package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/aryadhira/iris/internal/files"
	"github.com/aryadhira/iris/internal/rooms"
	"github.com/aryadhira/iris/internal/store"
)

const (
	maxUploadBytes = 25 << 20
	// Video and sound are not documents. Keeping one ceiling for everything meant a 26 MB clip
	// was rejected while the product promises to send files to Hermes.
	maxMediaBytes = 64 << 20
	// The read loop has to accept up to the largest ceiling; the per-type limit is enforced after sniffing.
	maxReadBytes = maxMediaBytes
)

func (s *Server) listRoomFiles(c echo.Context) error {
	id, err := roomID(c)
	if err != nil {
		return err
	}
	if _, err := s.store.GetRoom(c.Request().Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errorBody("Room itu tidak ada."))
		}
		s.logger.Error("cannot load room for files", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa membuka room itu."))
	}
	rows, err := s.store.ListFiles(c.Request().Context(), id)
	if err != nil {
		s.logger.Error("cannot list files", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa memuat file room ini."))
	}
	views := make([]fileView, 0, len(rows))
	for _, row := range rows {
		views = append(views, viewFile(row))
	}
	return c.JSON(http.StatusOK, map[string]any{"files": views})
}

// uploadFile accepts one multipart file, streams it to a temp file in the
// room's inbox, sniffs the bytes, and only then moves it into place.
func (s *Server) uploadFile(c echo.Context) error {
	id, err := roomID(c)
	if err != nil {
		return err
	}
	room, err := s.store.GetRoom(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errorBody("Room itu tidak ada."))
		}
		s.logger.Error("cannot load room for upload", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa membuka room itu."))
	}
	if room.FolderPath == "" {
		return c.JSON(http.StatusInternalServerError, errorBody("Room ini tidak punya folder. Buat room baru."))
	}

	folder := rooms.Open(room.FolderPath)
	part, err := firstFilePart(c.Request())
	if err != nil {
		return c.JSON(http.StatusBadRequest, errorBody("Kirim tepat satu file dalam form upload."))
	}
	defer part.Close()

	original := part.FileName()
	storedName := uuid.New().String() + "-" + rooms.SanitizeFilename(original)
	rel := path.Join("inbox", storedName)
	abs, err := folder.Resolve(rel)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errorBody("Nama file itu tidak bisa dipakai."))
	}

	tmp := abs + ".part"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		s.logger.Error("cannot open upload temp file", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa menyimpan file itu. Coba lagi."))
	}

	hash := sha256.New()
	head := make([]byte, 512)
	headLen := 0
	var size int64
	buf := make([]byte, 32*1024)
	writeFailed := false
	for {
		n, readErr := part.Read(buf)
		if n > 0 {
			size += int64(n)
			if headLen < len(head) {
				copied := copy(head[headLen:], buf[:n])
				headLen += copied
			}
			_, _ = hash.Write(buf[:n])
			if size <= maxReadBytes && !writeFailed {
				if _, err := out.Write(buf[:n]); err != nil {
					writeFailed = true
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			out.Close()
			_ = os.Remove(tmp)
			return c.JSON(http.StatusBadRequest, errorBody("File itu tidak bisa dibaca sampai selesai. Coba lagi."))
		}
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa menyimpan file itu. Coba lagi."))
	}
	if writeFailed {
		_ = os.Remove(tmp)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa menyimpan file itu. Coba lagi."))
	}
	if size > maxMediaBytes {
		_ = os.Remove(tmp)
		return c.JSON(http.StatusRequestEntityTooLarge, errorBody(fmt.Sprintf("%s — batasnya 64 MB", files.FormatSize(size))))
	}
	if size == 0 {
		_ = os.Remove(tmp)
		return c.JSON(http.StatusBadRequest, errorBody("File itu kosong."))
	}

	mimeType, err := files.DetectFile(tmp, original)
	if err != nil || !files.Allowed(mimeType) {
		_ = os.Remove(tmp)
		return c.JSON(http.StatusUnsupportedMediaType, errorBody("tipe file ini belum didukung"))
	}
	// The read loop accepts up to the media ceiling; a document must still respect 25 MB.
	if size > maxUploadBytes && !files.IsMedia(mimeType) {
		_ = os.Remove(tmp)
		return c.JSON(http.StatusRequestEntityTooLarge, errorBody(fmt.Sprintf("%s — batasnya 25 MB", files.FormatSize(size))))
	}
	if err := os.Rename(tmp, abs); err != nil {
		_ = os.Remove(tmp)
		s.logger.Error("cannot move upload into place", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa menyimpan file itu. Coba lagi."))
	}

	row, err := s.store.InsertFile(c.Request().Context(), store.InsertFileParams{
		ID:     uuid.New(),
		RoomID: room.ID,
		Source: "user",
		Name:   strings.TrimSpace(original),
		Path:   rel,
		Mime:   mimeType,
		Size:   size,
		Sha256: hex.EncodeToString(hash.Sum(nil)),
	})
	if err != nil {
		_ = os.Remove(abs)
		s.logger.Error("cannot save uploaded file", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa menyimpan file itu. Coba lagi."))
	}
	if strings.TrimSpace(row.Name) == "" {
		row.Name = storedName
	}
	return c.JSON(http.StatusCreated, map[string]any{"file": viewFile(row)})
}

func firstFilePart(r *http.Request) (*multipart.Part, error) {
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, err
	}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			return nil, errors.New("no file part")
		}
		if err != nil {
			return nil, err
		}
		if part.FileName() != "" {
			return part, nil
		}
		_ = part.Close()
	}
}

func (s *Server) downloadFile(c echo.Context) error {
	id, err := fileID(c)
	if err != nil {
		return err
	}
	row, err := s.store.GetFile(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errorBody("File itu tidak ada."))
		}
		s.logger.Error("cannot load file", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa membuka file itu."))
	}
	room, err := s.store.GetRoom(c.Request().Context(), row.RoomID)
	if err != nil {
		s.logger.Error("cannot load file room", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa membuka file itu."))
	}
	abs, err := rooms.Open(room.FolderPath).Resolve(row.Path)
	if err != nil {
		s.logger.Error("file path failed the room guard", "error", err, "path", row.Path)
		return c.JSON(http.StatusNotFound, errorBody("File itu tidak ada."))
	}
	file, err := os.Open(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c.JSON(http.StatusNotFound, errorBody("File itu tidak ada lagi di folder room."))
		}
		s.logger.Error("cannot open file", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa membuka file itu."))
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa membuka file itu."))
	}

	disposition := "attachment"
	if files.Inline(row.Mime) {
		disposition = "inline"
	}
	// A media file streams inline, but its download control must still ask for
	// an attachment. One URL, one explicit intent.
	if c.QueryParam("download") != "" {
		disposition = "attachment"
	}
	headers := c.Response().Header()
	headers.Set(echo.HeaderContentType, row.Mime)
	headers.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": row.Name}))
	headers.Set("X-Content-Type-Options", "nosniff")
	headers.Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	headers.Set("Cache-Control", "private, max-age=0, must-revalidate")

	http.ServeContent(c.Response(), c.Request(), row.Name, info.ModTime(), file)
	return nil
}

func (s *Server) deleteFile(c echo.Context) error {
	id, err := fileID(c)
	if err != nil {
		return err
	}
	row, err := s.store.DeleteFile(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errorBody("File itu tidak ada."))
		}
		s.logger.Error("cannot delete file row", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa menghapus file itu."))
	}

	// Row first: if the bytes cannot be removed, the library is still honest.
	// A leftover byte is reported, not hidden.
	room, err := s.store.GetRoom(c.Request().Context(), row.RoomID)
	if err == nil {
		if abs, resolveErr := rooms.Open(room.FolderPath).Resolve(row.Path); resolveErr == nil {
			if removeErr := os.Remove(abs); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				s.logger.Error("cannot remove file bytes", "error", removeErr, "path", row.Path)
			}
		} else {
			s.logger.Error("refusing to delete bytes outside the room", "error", resolveErr, "path", row.Path)
		}
	}

	return c.JSON(http.StatusOK, map[string]bool{"deleted": true})
}

func fileID(c echo.Context) (uuid.UUID, error) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return uuid.Nil, c.JSON(http.StatusNotFound, errorBody("File itu tidak ada."))
	}
	return id, nil
}

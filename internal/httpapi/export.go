package httpapi

import (
	"bytes"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"

	"github.com/aryadhira/iris/internal/files"
)

var indonesianMonths = [...]string{
	"Januari", "Februari", "Maret", "April", "Mei", "Juni",
	"Juli", "Agustus", "September", "Oktober", "November", "Desember",
}

func indonesianDate(t time.Time) string {
	return fmt.Sprintf("%d %s %d", t.Day(), indonesianMonths[int(t.Month())-1], t.Year())
}

func indonesianDateTime(t time.Time) string {
	return fmt.Sprintf("%s, %02d.%02d", indonesianDate(t), t.Hour(), t.Minute())
}

// exportRoom writes the transcript as markdown. Message bodies are copied
// verbatim, so fenced code blocks survive; irisd only adds labels and
// attachment lines.
func (s *Server) exportRoom(c echo.Context) error {
	id, err := roomID(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	room, err := s.store.GetRoom(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errorBody("Room itu tidak ada."))
		}
		s.logger.Error("cannot load room for export", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa mengekspor room ini."))
	}

	messages, err := s.store.ListAllMessages(ctx, room.ID)
	if err != nil {
		s.logger.Error("cannot list messages for export", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa memuat pesan room ini."))
	}
	fileRows, err := s.store.ListFiles(ctx, room.ID)
	if err != nil {
		s.logger.Error("cannot list files for export", "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody("Iris tidak bisa memuat lampiran room ini."))
	}
	byMessage := attachmentsByMessage(fileRows)

	var body bytes.Buffer
	fmt.Fprintf(&body, "# %s\n\n", room.Name)
	if room.Topic != "" {
		fmt.Fprintf(&body, "**Topik:** %s\n\n", room.Topic)
	}
	fmt.Fprintf(&body, "**Tanggal:** %s\n\n---\n\n", indonesianDate(time.Now()))

	if len(messages) == 0 {
		body.WriteString("_Room ini belum punya pesan._\n")
	}
	for _, message := range messages {
		label := "Sistem"
		switch message.Role {
		case "user":
			label = "Kamu"
		case "assistant":
			label = "Hermes"
		}
		fmt.Fprintf(&body, "**%s** · %s\n\n", label, indonesianDateTime(message.CreatedAt))
		if message.Body != "" {
			body.WriteString(message.Body)
			body.WriteString("\n\n")
		}
		attachments := byMessage[message.ID]
		if len(attachments) > 0 {
			body.WriteString("**Lampiran:**\n\n")
			for _, file := range attachments {
				fmt.Fprintf(&body, "- %s (%s)\n", file.Name, files.FormatSize(file.Size))
			}
			body.WriteString("\n")
		}
		body.WriteString("---\n\n")
	}

	filename := fmt.Sprintf("%s-%s.md", exportSlug(room.Name, room.ID.String()), time.Now().Format("2006-01-02"))
	c.Response().Header().Set("Content-Type", "text/markdown; charset=utf-8")
	c.Response().Header().Set("X-Content-Type-Options", "nosniff")
	c.Response().Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	return c.Blob(http.StatusOK, "text/markdown; charset=utf-8", body.Bytes())
}

// exportSlug keeps the filename safe on every filesystem and avoids header
// injection; an all-symbol room name falls back to its id.
func exportSlug(name, id string) string {
	var out strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out.WriteRune(r)
			lastDash = false
		case r == ' ' || r == '-' || r == '_':
			if !lastDash && out.Len() > 0 {
				out.WriteByte('-')
				lastDash = true
			}
		}
		if out.Len() >= 48 {
			break
		}
	}
	slug := strings.Trim(out.String(), "-")
	if slug == "" {
		return id
	}
	return slug
}

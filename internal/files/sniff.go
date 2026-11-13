// Package files identifies uploaded bytes and formats the human labels Iris
// uses. Nothing here trusts a client's Content-Type.
package files

import (
	"archive/zip"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"
)

const (
	MIMEPNG   = "image/png"
	MIMEJPEG  = "image/jpeg"
	MIMEWEBP  = "image/webp"
	MIMEGIF   = "image/gif"
	MIMEPDF   = "application/pdf"
	MIMEDOCX  = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	MIMEXLSX  = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	MIMEText  = "text/plain"
	MIMEMD    = "text/markdown"
	MIMECSV   = "text/csv"
	MIMEJSON  = "application/json"
	MIMEZip   = "application/zip"
	MIMEMP4   = "video/mp4"
	MIMEMOV   = "video/quicktime"
	MIMEWEBM  = "video/webm"
	MIMEOther = "application/octet-stream"
)

var allowed = map[string]bool{
	MIMEPNG:  true,
	MIMEJPEG: true,
	MIMEWEBP: true,
	MIMEGIF:  true,
	MIMEPDF:  true,
	MIMEDOCX: true,
	MIMEXLSX: true,
	MIMEText: true,
	MIMEMD:   true,
	MIMECSV:  true,
	MIMEJSON: true,
	MIMEZip:  true,
	MIMEMP4:  true,
	MIMEMOV:  true,
	MIMEWEBM: true,
}

// IsVideo reports whether an upload may use the larger video ceiling.
func IsVideo(mimeType string) bool {
	switch mimeType {
	case MIMEMP4, MIMEMOV, MIMEWEBM:
		return true
	}
	return false
}

// sniffVideo identifies the container formats net/http cannot: DetectContentType answers
// application/octet-stream for every ISO base media file, so an mp4 was rejected as "not supported"
// while it was a perfectly ordinary video.
func sniffVideo(head []byte) string {
	// ISO base media (mp4, m4v, mov): a box header at 0..8 whose type is "ftyp", then a brand.
	if len(head) >= 12 && string(head[4:8]) == "ftyp" {
		brand := string(head[8:12])
		switch brand {
		case "qt  ":
			return MIMEMOV
		case "isom", "iso2", "iso4", "iso5", "iso6", "mp41", "mp42", "avc1", "M4V ", "M4A ", "dash":
			return MIMEMP4
		default:
			return MIMEMP4 // any other ftyp is still an ISO media container; ext decides mov vs mp4 below
		}
	}
	// Matroska / WebM: EBML magic.
	if len(head) >= 4 && head[0] == 0x1A && head[1] == 0x45 && head[2] == 0xDF && head[3] == 0xA3 {
		return MIMEWEBM
	}
	return ""
}

// Allowed reports whether an upload of this MIME may be stored.
func Allowed(mimeType string) bool {
	return allowed[mimeType]
}

// DetectFile sniffs a file on disk. It reads at most a few hundred bytes plus
// the zip directory, never the whole body.
func DetectFile(filePath, name string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	head := make([]byte, 512)
	n, err := io.ReadFull(file, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", err
	}
	return detect(head[:n], name, filePath), nil
}

func detect(head []byte, name, filePath string) string {
	if video := sniffVideo(head); video != "" {
		// A ".mov" whose brand is generic is a QuickTime file in practice.
		if video == MIMEMP4 && strings.EqualFold(path.Ext(name), ".mov") {
			return MIMEMOV
		}
		return video
	}
	detected := http.DetectContentType(head)
	if mediaType, _, err := mime.ParseMediaType(detected); err == nil {
		detected = mediaType
	}

	switch detected {
	case MIMEZip:
		if office := officeMIME(filePath); office != "" {
			return office
		}
		return MIMEZip
	case MIMEText:
		switch strings.ToLower(path.Ext(name)) {
		case ".md", ".markdown":
			return MIMEMD
		case ".csv":
			return MIMECSV
		case ".json":
			return MIMEJSON
		}
		return MIMEText
	default:
		return detected
	}
}

func officeMIME(filePath string) string {
	reader, err := zip.OpenReader(filePath)
	if err != nil {
		return ""
	}
	defer reader.Close()
	var word, sheet bool
	for _, entry := range reader.File {
		switch {
		case strings.HasPrefix(entry.Name, "word/"):
			word = true
		case strings.HasPrefix(entry.Name, "xl/"):
			sheet = true
		}
	}
	switch {
	case word:
		return MIMEDOCX
	case sheet:
		return MIMEXLSX
	default:
		return ""
	}
}

// HumanType is the short label used in the attachment line and in the UI.
func HumanType(mimeType, name string) string {
	switch mimeType {
	case MIMEPNG:
		return "PNG"
	case MIMEJPEG:
		return "JPEG"
	case MIMEWEBP:
		return "WEBP"
	case MIMEGIF:
		return "GIF"
	case MIMEMP4:
		return "MP4"
	case MIMEMOV:
		return "MOV"
	case MIMEWEBM:
		return "WEBM"
	case MIMEPDF:
		return "PDF"
	case MIMEDOCX:
		return "DOCX"
	case MIMEXLSX:
		return "XLSX"
	case MIMEMD:
		return "Markdown"
	case MIMECSV:
		return "CSV"
	case MIMEJSON:
		return "JSON"
	case MIMEZip:
		return "ZIP"
	case MIMEText:
		return "TXT"
	}
	ext := strings.TrimPrefix(strings.ToUpper(path.Ext(name)), ".")
	if ext != "" && len(ext) <= 8 {
		return ext
	}
	return "Berkas"
}

// FormatSize writes Indonesian human sizes: "840 KB", "1,2 MB".
func FormatSize(size int64) string {
	const (
		kb = int64(1024)
		mb = kb * 1024
	)
	switch {
	case size < kb:
		return fmt.Sprintf("%d B", size)
	case size < mb:
		value := float64(size) / float64(kb)
		if value < 10 {
			return comma(fmt.Sprintf("%.1f KB", value))
		}
		return fmt.Sprintf("%.0f KB", value)
	default:
		value := float64(size) / float64(mb)
		if value < 100 {
			return comma(fmt.Sprintf("%.1f MB", value))
		}
		return fmt.Sprintf("%.0f MB", value)
	}
}

func comma(value string) string {
	return strings.ReplaceAll(value, ".", ",")
}

// Inline reports whether a browser should render the file instead of
// downloading it.
func Inline(mimeType string) bool {
	return strings.HasPrefix(mimeType, "image/") || mimeType == MIMEPDF
}

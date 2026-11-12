package rooms

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

const (
	inboxName  = "inbox"
	outboxName = "outbox"
)

// ContinuityPrompt is the part of the room contract that keeps a job from
// being forgotten. It is prepended to every turn (ADR-003, wait in-turn is the
// primary layer; the pump in irisd is only the safety net).
const ContinuityPrompt = `Kalau kamu memulai pekerjaan panjang, utamakan menunggu di turn ini dengan process_manage wait lalu laporkan hasilnya — turn yang lama tidak apa-apa.
Kalau kamu sengaja membiarkannya berjalan, sebutkan persis apa yang berjalan dan kapan selesainya.
Akhiri balasan dengan satu baris: STATUS: RUNNING — <apa dan berapa lama>, STATUS: DONE — <hasil>, atau STATUS: BLOCKED — <yang kamu butuhkan>.
Jangan pernah mengaku selesai selama pekerjaan yang kamu mulai masih berjalan.`

// ErrUnsafePath is returned when a path would leave its room folder.
var ErrUnsafePath = errors.New("path escapes the room folder")

// Folder is one room's private directory. Root is an absolute path.
type Folder struct {
	Root string
}

// Open wraps an already-known room root (for example one read from the room
// row). It does not create anything.
func Open(root string) Folder {
	return Folder{Root: root}
}

// Create makes rooms/<room-id>/inbox and rooms/<room-id>/outbox under dataDir
// and returns the absolute room root.
func Create(dataDir string, roomID uuid.UUID) (Folder, error) {
	root, err := filepath.Abs(dataDir)
	if err != nil {
		return Folder{}, fmt.Errorf("resolve data dir: %w", err)
	}
	roomRoot := filepath.Join(root, "rooms", roomID.String())
	for _, dir := range []string{filepath.Join(roomRoot, inboxName), filepath.Join(roomRoot, outboxName)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Folder{}, fmt.Errorf("create room folder: %w", err)
		}
	}
	return Folder{Root: roomRoot}, nil
}

// Inbox is where files from Boss arrive.
func (f Folder) Inbox() string { return filepath.Join(f.Root, inboxName) }

// Outbox is where Hermes leaves deliverables.
func (f Folder) Outbox() string { return filepath.Join(f.Root, outboxName) }

// Remove deletes the room folder tree. Used to undo a failed room creation.
func (f Folder) Remove() error {
	if f.Root == "" {
		return nil
	}
	return os.RemoveAll(f.Root)
}

// SystemPrompt is the short Indonesian contract prepended to every turn.
func (f Folder) SystemPrompt() string {
	return strings.Join([]string{
		"Kamu adalah Hermes, bekerja di dalam room Iris ini.",
		"Folder room ini: " + f.Root + ".",
		"File yang Boss kirim ada di inbox/ — baca dengan alat file-mu.",
		"Simpan hasil yang harus Boss terima di outbox/.",
		"Akhiri balasanmu dengan satu baris [[artifact: outbox/<nama>]] untuk setiap file yang harus Boss terima.",
		"Jangan menulis di luar folder room ini.",
		"Jawab dalam bahasa yang Boss pakai. Kalau Boss menulis bahasa Indonesia, jawab bahasa Indonesia — jangan dicampur.",
		ContinuityPrompt,
	}, "\n")
}

// TurnInstructions returns the prompt sent to Hermes for one turn. Rooms
// created before P3 have a stored contract without the continuity lines, so
// the four lines are appended when missing instead of silently not applying to
// every existing room.
func TurnInstructions(stored string) string {
	stored = strings.TrimSpace(stored)
	if strings.Contains(stored, "STATUS: RUNNING") {
		return stored
	}
	if stored == "" {
		return ContinuityPrompt
	}
	return stored + "\n" + ContinuityPrompt
}

// Resolve turns a relative room path into an absolute path, refusing anything
// that could leave the room: absolute paths, any ".." component, the root
// itself, and symlinks (including a symlinked parent).
func (f Folder) Resolve(rel string) (string, error) {
	if strings.TrimSpace(rel) == "" || strings.IndexByte(rel, 0) >= 0 {
		return "", ErrUnsafePath
	}
	if f.Root == "" {
		return "", ErrUnsafePath
	}

	slashed := strings.ReplaceAll(rel, "\\", "/")
	if strings.HasPrefix(slashed, "/") || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return "", ErrUnsafePath
	}
	for _, part := range strings.Split(slashed, "/") {
		if part == "" || part == "." || part == ".." {
			return "", ErrUnsafePath
		}
	}
	clean := path.Clean(slashed)
	if clean == "." || clean != slashed {
		return "", ErrUnsafePath
	}

	rootReal, err := filepath.EvalSymlinks(f.Root)
	if err != nil {
		return "", fmt.Errorf("resolve room root: %w", err)
	}
	target := filepath.Join(f.Root, filepath.FromSlash(clean))
	if !within(rootReal, target) {
		return "", ErrUnsafePath
	}

	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", ErrUnsafePath
		}
		real, err := filepath.EvalSymlinks(target)
		if err != nil {
			return "", ErrUnsafePath
		}
		if !within(rootReal, real) {
			return "", ErrUnsafePath
		}
		return target, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", ErrUnsafePath
	}

	// The file does not exist yet. Its deepest existing parent must still be
	// inside the room after symlinks are resolved.
	parent := filepath.Dir(target)
	for {
		if info, err := os.Lstat(parent); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return "", ErrUnsafePath
			}
			real, err := filepath.EvalSymlinks(parent)
			if err != nil || !within(rootReal, real) {
				return "", ErrUnsafePath
			}
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", ErrUnsafePath
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", ErrUnsafePath
		}
		parent = next
	}

	return target, nil
}

func within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// SanitizeFilename makes a display name safe to store on disk. It never
// returns a path separator and never returns an empty string.
func SanitizeFilename(original string) string {
	name := strings.ReplaceAll(original, "\\", "/")
	name = path.Base(name)
	name = strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\':
			return '-'
		case r == 0 || unicode.IsControl(r):
			return -1
		default:
			return r
		}
	}, name)
	name = strings.Trim(name, " .")
	if name == "" {
		name = "berkas"
	}
	if len([]rune(name)) > 180 {
		ext := path.Ext(name)
		if len([]rune(ext)) > 20 {
			ext = ""
		}
		base := strings.TrimSuffix(name, ext)
		baseRunes := []rune(base)
		limit := 180 - len([]rune(ext))
		if limit < 1 {
			limit = 180
			ext = ""
		}
		if len(baseRunes) > limit {
			base = string(baseRunes[:limit])
		}
		name = base + ext
	}
	return name
}

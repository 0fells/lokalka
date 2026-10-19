package stamp

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
)

const magic = "Lokalka!"

type File struct {
	Version int    `json:"lokalka"`
	Server  string `json:"server"`

	ServerKey string `json:"server_key,omitempty"`
	Network   string `json:"network"`
	Password  string `json:"password"`
	ConfigID  string `json:"config_id,omitempty"`
	Label     string `json:"label,omitempty"`

	Role    string `json:"role,omitempty"`
	AdminID string `json:"admin_id,omitempty"`
}

const RolePanel = "panel"

func Append(base []byte, file File) ([]byte, error) {
	file.Version = 1
	if file.Server == "" || file.Network == "" || file.Password == "" {
		return nil, errors.New("в файле подключения нет адреса, сети или пароля")
	}
	payload, err := json.Marshal(file)
	if err != nil {
		return nil, err
	}
	if len(payload) > 1<<20 {
		return nil, errors.New("файл подключения слишком большой")
	}
	base = Strip(base)
	var lenb [4]byte
	binary.LittleEndian.PutUint32(lenb[:], uint32(len(payload)))
	out := make([]byte, 0, len(base)+len(payload)+len(lenb)+len(magic))
	out = append(out, base...)
	out = append(out, payload...)
	out = append(out, lenb[:]...)
	out = append(out, magic...)
	return out, nil
}

func Strip(b []byte) []byte {
	_, ok, _ := Parse(b)
	if !ok {
		return b
	}
	tail, _, _, ok := split(b)
	if !ok {
		return b
	}
	return b[:len(b)-tail]
}

func Parse(b []byte) (File, bool, error) {
	payload, _, _, ok := split(b)
	if !ok {
		return File{}, false, nil
	}
	start := len(b) - payload
	raw := b[start : len(b)-12]
	var file File
	if err := json.Unmarshal(raw, &file); err != nil || file.Version != 1 || file.Server == "" || file.Network == "" || file.Password == "" {
		return File{}, false, errors.New("файл подключения повреждён")
	}
	return file, true, nil
}

func ReadFile(path string) (File, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return File{}, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return File{}, false, err
	}
	if info.Size() < 12 || info.Size() > 1<<31 {
		return File{}, false, nil
	}
	tail := make([]byte, 12)
	if _, err := f.ReadAt(tail, info.Size()-12); err != nil {
		return File{}, false, err
	}
	if string(tail[4:]) != magic {
		return File{}, false, nil
	}
	n := binary.LittleEndian.Uint32(tail[:4])
	if n == 0 || n > 1<<20 || int64(n)+12 > info.Size() {
		return File{}, false, errors.New("файл подключения повреждён")
	}
	raw := make([]byte, n)
	if _, err := f.ReadAt(raw, info.Size()-12-int64(n)); err != nil {
		return File{}, false, err
	}
	var file File
	if err := json.Unmarshal(raw, &file); err != nil || file.Version != 1 || file.Server == "" || file.Network == "" || file.Password == "" {
		return File{}, false, errors.New("файл подключения повреждён")
	}
	return file, true, nil
}

func split(b []byte) (payload, length, mag int, ok bool) {
	if len(b) < 12 || string(b[len(b)-len(magic):]) != magic {
		return 0, 0, 0, false
	}
	n := binary.LittleEndian.Uint32(b[len(b)-12 : len(b)-8])
	if n == 0 || int(n) > len(b)-12 || n > 1<<20 {
		return 0, 0, 0, false
	}
	return int(n) + 12, 0, 0, true
}

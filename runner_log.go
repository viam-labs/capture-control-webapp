package capturecontrolwebapp

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// segment is one captured (or attempted) run: a policy run or a correction.
type segment struct {
	Session    string    `json:"session"`
	Episode    int       `json:"episode"`
	Source     string    `json:"source"` // "policy" | "correction"
	Task       string    `json:"task"`
	Checkpoint string    `json:"checkpoint"`
	TStart     time.Time `json:"t_start"`
	TEnd       time.Time `json:"t_end"`
	Outcome    string    `json:"outcome"` // success | failure | takeover | error | stopped
	Error      string    `json:"error"`
	Captured   bool      `json:"captured"` // a start_capture was sent: a synced sequence may exist
}

func logPath(dir, session string) string { return filepath.Join(dir, session+".jsonl") }

func appendSegment(dir string, s segment) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(logPath(dir, s.Session), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

func readSegments(dir, session string) ([]segment, error) {
	f, err := os.Open(logPath(dir, session))
	if os.IsNotExist(err) {
		return []segment{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := []segment{}
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var s segment
		if json.Unmarshal(sc.Bytes(), &s) == nil {
			out = append(out, s)
		}
	}
	return out, sc.Err()
}

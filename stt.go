package main

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Dictation for the chat boxes, in-house. The page records a clip and posts it here; faster-whisper (in its own
// venv, ~/.local/opt/stt) turns it into text on this machine, nothing leaves it. One worker process keeps the
// model loaded between clips (the first clip pays ~1 s to load it) and goes away after sttIdle without one.

// sttWorkerSrc is the worker script, written next to the clips when the worker starts.
//
//go:embed stt_worker.py
var sttWorkerSrc []byte

const (
	sttIdle    = 20 * time.Minute // the worker goes away after this long without a clip
	sttMaxClip = 25 << 20         // bytes: ~25 min of opus
	// sttHotwords are words the model would otherwise mishear, as the owner says them. As hotwords, not an initial
	// prompt: on accented test speech both fix the names (zellij, worktree, Codex, JARVIS), but the prompt also cost
	// words on everyday talk. Only names: plain words here ("the control panel") got tacked onto clips about
	// something else. The final period keeps the model punctuating.
	sttHotwords = "Claude, Codex, JARVIS, Hermes, zellij, Terminals, CI, PR, repo, deploy, commit, worktree, VNC, Telegram, Opus."
)

// stt runs the dictation worker and feeds it clips one at a time.
type stt struct {
	python, dir string

	mu   sync.Mutex // one clip at a time through the worker
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Scanner
	idle *time.Timer
}

// newSTT returns a dictation service that keeps its clips in dir and uses the venv in ~/.local/opt/stt.
func newSTT(dir string) *stt {
	py := os.ExpandEnv("$HOME/.local/opt/stt/bin/python")
	return &stt{python: py, dir: dir}
}

// ready reports whether the dictation venv is installed.
func (t *stt) ready() bool { _, err := os.Stat(t.python); return err == nil }

// start launches the worker if it isn't running and waits for its model to load (call with t.mu held).
func (t *stt) start() error {
	if t.cmd != nil {
		return nil
	}
	script := filepath.Join(t.dir, "stt_worker.py")
	if err := os.WriteFile(script, sttWorkerSrc, 0o600); err != nil {
		return err
	}
	cmd := exec.Command(t.python, "-I", script)
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		ff = os.ExpandEnv("$HOME/.pixi/bin/ffmpeg")
	}
	// the model is in the cache: never fetch it at dictation time
	// small.en on 4 threads: on accented speech ~a quarter fewer word errors than base, a 30 s clip in ~4-6 s on a
	// busy machine. Not one thread per core: with other work running, OpenMP threads spin waiting on each other and
	// a clip that took 4 s on 4 threads took 7-80 s on 8 or 16. Bigger models (large-v3-turbo, distil-large-v3.5)
	// read better still but took 13-40 s a clip.
	cmd.Env = append(os.Environ(), "HF_HUB_OFFLINE=1", "STT_FFMPEG="+ff, "STT_MODEL=small.en", "STT_THREADS=4")
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	if !sc.Scan() { // {"ready": true} once the model is loaded
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return errors.New("the dictation model didn't load")
	}
	t.cmd, t.in, t.out = cmd, in, sc
	return nil
}

// stop kills the worker if it is running (call with t.mu held).
func (t *stt) stop() {
	if t.cmd == nil {
		return
	}
	_ = t.in.Close()
	_ = t.cmd.Process.Kill()
	_ = t.cmd.Wait()
	t.cmd, t.in, t.out = nil, nil, nil
}

// transcribe turns the audio clip at path into text, starting the worker if needed and stopping it after sttIdle.
func (t *stt) transcribe(path string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.start(); err != nil {
		return "", err
	}
	if t.idle != nil {
		t.idle.Stop()
	}
	t.idle = time.AfterFunc(sttIdle, func() { t.mu.Lock(); t.stop(); t.mu.Unlock() })
	req, _ := json.Marshal(map[string]string{"path": path, "lang": "en", "hotwords": sttHotwords})
	if _, err := t.in.Write(append(req, '\n')); err != nil {
		t.stop()
		return "", err
	}
	if !t.out.Scan() {
		t.stop()
		return "", errors.New("the dictation worker stopped")
	}
	var res struct{ Text, Error string }
	if err := json.Unmarshal(t.out.Bytes(), &res); err != nil {
		return "", err
	}
	if res.Error != "" {
		return "", errors.New(res.Error)
	}
	return res.Text, nil
}

// sttPost serves POST /api/stt: the clip as the body (webm/ogg/mp4/wav, whatever the browser records) → {"text": ...}.
func (s *server) sttPost(w http.ResponseWriter, r *http.Request) {
	if !s.stt.ready() {
		writeJSON(w, 503, map[string]string{"error": "Dictation isn't set up on this machine (no ~/.local/opt/stt)"})
		return
	}
	f, err := os.CreateTemp(s.stt.dir, "clip-*")
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	defer os.Remove(f.Name())
	n, err := io.Copy(f, http.MaxBytesReader(w, r.Body, sttMaxClip))
	f.Close()
	if err != nil || n == 0 {
		writeJSON(w, 400, map[string]string{"error": fmt.Sprintf("No clip received (%v)", err)})
		return
	}
	t0 := time.Now()
	text, err := s.stt.transcribe(f.Name())
	if err != nil {
		log.Printf("stt: %v", err)
		writeJSON(w, 500, map[string]string{"error": "Couldn't transcribe: " + err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"text": text, "ms": time.Since(t0).Milliseconds()})
}

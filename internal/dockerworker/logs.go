package dockerworker

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"workstation-manager/pkg/workerapi"
)

var logFileID = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)

const (
	maxLogFileBytes = int64(25 * 1024 * 1024)
	maxLogReadBytes = int64(2 * 1024 * 1024)
)

// StartLogCapture resumes collectors for existing resources and supplies the
// lifetime used by collectors created during later provisioning.
func (s *Service) StartLogCapture(ctx context.Context) {
	if s.engine == nil || s.config.LogsDirectory == "" {
		return
	}
	s.captureMu.Lock()
	s.captureCtx = ctx
	s.captureMu.Unlock()
	go s.resumeLogCapture(ctx)
}

func (s *Service) resumeLogCapture(ctx context.Context) {
	resources, err := s.engine.ListManaged(ctx, "")
	if err != nil {
		s.log.Warn("resume workstation log capture", "error", err)
		return
	}
	for _, resource := range resources {
		if resource.State == "running" {
			s.captureResource(resource)
		}
	}
}

func (s *Service) captureResource(resource workerapi.Resource) {
	var logName string
	switch resource.Kind {
	case "app":
		logName = resource.AppID
	case "wslan":
		logName = "wslan"
	case "sandbox":
		logName = "network-" + resource.AppID
	default:
		return
	}
	s.captureContainer(resource.WorkstationID, logName, resource.Name)
}

func (s *Service) captureContainer(workstationID, logName, containerName string) {
	if s.engine == nil || s.config.LogsDirectory == "" ||
		!resourceID.MatchString(workstationID) || !logFileID.MatchString(logName) {
		return
	}
	s.captureMu.Lock()
	ctx := s.captureCtx
	if ctx == nil {
		s.captureMu.Unlock()
		return
	}
	if _, exists := s.captures[containerName]; exists {
		s.captureMu.Unlock()
		return
	}
	s.captures[containerName] = struct{}{}
	s.captureMu.Unlock()
	go func() {
		defer func() {
			s.captureMu.Lock()
			delete(s.captures, containerName)
			s.captureMu.Unlock()
		}()
		if err := s.captureLoop(ctx, workstationID, logName, containerName); err != nil &&
			!errors.Is(err, context.Canceled) {
			s.log.Warn("workstation log capture stopped",
				"workstation_id", workstationID, "resource", logName, "error", err)
		}
	}()
}

func (s *Service) captureLoop(ctx context.Context, workstationID, logName, containerName string) error {
	path, err := s.prepareLogPath(workstationID, logName)
	if err != nil {
		return err
	}
	writer, err := newRotatingLogWriter(path, maxLogFileBytes,
		s.config.LogsUID, s.config.LogsGID)
	if err != nil {
		return err
	}
	defer writer.Close()

	since := int64(0)
	if info, statErr := os.Stat(path); statErr == nil && info.Size() > 0 {
		since = info.ModTime().Add(-2 * time.Second).Unix()
	}
	for {
		err = s.engine.StreamContainerLogs(ctx, containerName, since, writer)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		check, cancel := context.WithTimeout(ctx, 5*time.Second)
		running, _, _, stateErr := s.engine.ContainerState(check, containerName)
		cancel()
		if stateErr != nil || !running {
			return err
		}
		if err != nil {
			s.log.Debug("reconnecting Docker log stream", "container", containerName, "error", err)
		}
		since = time.Now().Add(-2 * time.Second).Unix()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (s *Service) prepareLogPath(workstationID, logName string) (string, error) {
	directory := filepath.Join(s.config.LogsDirectory, workstationID)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return "", err
	}
	_ = os.Chmod(directory, 0o750)
	if s.config.LogsUID >= 0 && s.config.LogsGID >= 0 {
		_ = os.Chown(directory, s.config.LogsUID, s.config.LogsGID)
	}
	return filepath.Join(directory, logName+".log"), nil
}

func (s *Service) persistedLogs(workstationID, appID string, tail int) (string, bool) {
	if s.config.LogsDirectory == "" || !resourceID.MatchString(workstationID) ||
		!logFileID.MatchString(appID) {
		return "", false
	}
	path := filepath.Join(s.config.LogsDirectory, workstationID, appID+".log")
	data, err := readLogTail(path, maxLogReadBytes)
	if err != nil || len(data) == 0 {
		return "", false
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > tail+1 {
		lines = lines[len(lines)-tail-1:]
	}
	return strings.Join(lines, "\n"), true
}

func readLogTail(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	offset := info.Size() - limit
	if offset < 0 {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(file, limit))
}

type rotatingLogWriter struct {
	path string
	file *os.File
	size int64
	max  int64
	uid  int
	gid  int
}

func newRotatingLogWriter(path string, max int64, uid, gid int) (*rotatingLogWriter, error) {
	writer := &rotatingLogWriter{path: path, max: max, uid: uid, gid: gid}
	if err := writer.open(os.O_CREATE | os.O_APPEND | os.O_WRONLY); err != nil {
		return nil, err
	}
	return writer, nil
}

func (w *rotatingLogWriter) open(flags int) error {
	file, err := os.OpenFile(w.path, flags, 0o640)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	w.file, w.size = file, info.Size()
	_ = os.Chmod(w.path, 0o640)
	if w.uid >= 0 && w.gid >= 0 {
		_ = os.Chown(w.path, w.uid, w.gid)
	}
	return nil
}

func (w *rotatingLogWriter) Write(data []byte) (int, error) {
	if w.size > 0 && w.size+int64(len(data)) > w.max {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	count, err := w.file.Write(data)
	w.size += int64(count)
	return count, err
}

func (w *rotatingLogWriter) rotate() error {
	if err := w.file.Close(); err != nil {
		return err
	}
	_ = os.Remove(w.path + ".1")
	if err := os.Rename(w.path, w.path+".1"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return w.open(os.O_CREATE | os.O_TRUNC | os.O_WRONLY)
}

func (w *rotatingLogWriter) Close() error {
	if w.file == nil {
		return nil
	}
	return w.file.Close()
}

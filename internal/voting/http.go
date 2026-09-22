package voting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type BuildInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
}

type HTTPService struct {
	service   *Service
	config    RuntimeConfig
	build     BuildInfo
	startedAt time.Time
}

const maxControlRequestBody = 1 << 20

func NewHTTPService(service *Service, config RuntimeConfig, build BuildInfo) *HTTPService {
	return &HTTPService{service: service, config: config, build: build, startedAt: time.Now().UTC()}
}

func (server *HTTPService) StatusHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", server.handleHealth)
	mux.HandleFunc("GET /readyz", server.handleReady)
	mux.HandleFunc("GET /v1/status", server.handleStatus)
	mux.HandleFunc("GET /v1/races/{race_id}", server.handleRace)
	return secureReadOnly(mux)
}

func (server *HTTPService) ControlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", server.handleStatus)
	mux.HandleFunc("POST /v1/import-plan", server.handleImportPlan)
	mux.HandleFunc("POST /v1/import-day", server.handleImportDay)
	mux.HandleFunc("POST /v1/arm-test", server.handleArm)
	mux.HandleFunc("POST /v1/arm-day", server.handleArmDay)
	mux.HandleFunc("POST /v1/auth-check", server.handleAuthCheck)
	mux.HandleFunc("POST /v1/check", server.handleCheck)
	mux.HandleFunc("POST /v1/kill", server.handleKill)
	mux.HandleFunc("POST /v1/cancel-unposted", server.handleCancelUnposted)
	return secureHeaders(mux)
}

func secureReadOnly(next http.Handler) http.Handler {
	return secureHeaders(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"error": "status server is read-only"})
			return
		}
		next.ServeHTTP(writer, request)
	}))
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(writer, request)
	})
}

func (server *HTTPService) handleHealth(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{
		"status":         "ok",
		"service":        "votingd",
		"started_at":     server.startedAt,
		"uptime_seconds": time.Since(server.startedAt).Seconds(),
	})
}

func (server *HTTPService) handleReady(writer http.ResponseWriter, _ *http.Request) {
	status := http.StatusOK
	result := map[string]any{
		"status":     "ready",
		"policy_id":  server.config.Policy.PolicyID,
		"go_version": runtime.Version(),
	}
	if runtime.Version() != "go1.26.6" {
		status = http.StatusServiceUnavailable
		result["status"] = "not_ready"
		result["error"] = "binary was not built with go1.26.6"
	}
	if _, err := os.Stat(server.config.StateDir); err != nil {
		status = http.StatusServiceUnavailable
		result["status"] = "not_ready"
		result["error"] = "state directory is unavailable"
	}
	writeJSON(writer, status, result)
}

func (server *HTTPService) handleStatus(writer http.ResponseWriter, _ *http.Request) {
	snapshot := server.service.store.Snapshot()
	mode := snapshot.Mode
	for _, record := range snapshot.Races {
		if record.Armed {
			if server.config.Policy.Submission.MaxRaces > 1 {
				mode = "FULL_DAY_ARMED"
			} else {
				mode = "TEST_ARMED"
			}
			break
		}
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"service":     "votingd",
		"build":       server.build,
		"go_version":  runtime.Version(),
		"policy_id":   server.config.Policy.PolicyID,
		"mode":        mode,
		"killed":      snapshot.Killed,
		"kill_reason": snapshot.KillReason,
		"sequence":    snapshot.Sequence,
		"updated_at":  snapshot.UpdatedAt,
		"races":       snapshot.Races,
	})
}

func (server *HTTPService) handleRace(writer http.ResponseWriter, request *http.Request) {
	raceID := request.PathValue("race_id")
	if !raceIDPattern.MatchString(raceID) {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "invalid race_id"})
		return
	}
	result := make([]*RaceRecord, 0, 1)
	for _, record := range server.service.store.Snapshot().Races {
		if record.Plan.Payload.RaceID == raceID {
			result = append(result, record)
		}
	}
	if len(result) == 0 {
		writeJSON(writer, http.StatusNotFound, map[string]any{"error": "race_id not found"})
		return
	}
	writeJSON(writer, http.StatusOK, result[0])
}

func (server *HTTPService) handleImportPlan(writer http.ResponseWriter, request *http.Request) {
	body, err := readRequestBody(request)
	if err != nil || len(body) == 0 {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "plan body is required"})
		return
	}
	record, err := server.service.ImportPlan(body)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusCreated, record)
}

func (server *HTTPService) handleImportDay(writer http.ResponseWriter, request *http.Request) {
	var bundle PlanBundle
	if err := decodeRequest(request, &bundle); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	records, err := server.service.ImportBundle(bundle)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{
		"bundle_id": bundle.BundleID,
		"count":     len(records),
		"records":   records,
	})
}

func (server *HTTPService) handleArm(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		PlanID        string `json:"plan_id"`
		ConfirmSHA256 string `json:"confirm_sha256"`
	}
	if err := decodeRequest(request, &input); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	record, err := server.service.Arm(input.PlanID, input.ConfirmSHA256)
	if err != nil {
		writeJSON(writer, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, record)
}

func (server *HTTPService) handleArmDay(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Bundle        PlanBundle `json:"bundle"`
		ConfirmSHA256 string     `json:"confirm_sha256"`
	}
	if err := decodeRequest(request, &input); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	records, err := server.service.ArmBundle(input.Bundle, input.ConfirmSHA256)
	if err != nil {
		writeJSON(writer, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"bundle_id": input.Bundle.BundleID,
		"armed":     len(records),
		"records":   records,
	})
}

func (server *HTTPService) handleAuthCheck(writer http.ResponseWriter, request *http.Request) {
	if err := server.service.AuthCheck(request.Context()); err != nil {
		writeJSON(writer, http.StatusBadGateway, map[string]any{"status": "failed", "error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"status": "ok"})
}

func (server *HTTPService) handleCheck(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		RaceID string `json:"race_id"`
	}
	if err := decodeRequest(request, &input); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	votes, err := server.service.CheckRace(request.Context(), input.RaceID)
	if err != nil {
		if IsEmptyPrecheckResponse(err) {
			writeJSON(writer, http.StatusOK, map[string]any{
				"status": "ok", "race_id": input.RaceID, "count": 0,
				"votes": []CheckedVote{}, "classification": "no_existing_vote",
			})
			return
		}
		writeJSON(writer, http.StatusBadGateway, map[string]any{"status": "failed", "error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"status": "ok", "race_id": input.RaceID, "count": len(votes), "votes": votes})
}

func (server *HTTPService) handleKill(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Reason string `json:"reason"`
	}
	if err := decodeRequest(request, &input); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := server.service.Kill(input.Reason); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"status": "killed"})
}

func (server *HTTPService) handleCancelUnposted(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		PlanID string `json:"plan_id"`
	}
	if err := decodeRequest(request, &input); err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := server.service.CancelUnposted(input.PlanID); err != nil {
		writeJSON(writer, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"status": "cancelled_unposted"})
}

func decodeRequest(request *http.Request, target any) error {
	body, err := readRequestBody(request)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request contains multiple JSON values")
	}
	return nil
}

func readRequestBody(request *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(request.Body, maxControlRequestBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxControlRequestBody {
		return nil, errors.New("request body exceeds size limit")
	}
	return body, nil
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}

func NewStatusServer(config RuntimeConfig, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              config.Policy.Interfaces.StatusListen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}

func ListenControlSocket(path string) (net.Listener, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := rejectSymlinkComponents(absolute); err != nil {
		return nil, err
	}
	path = absolute
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("refusing to replace non-socket control path %s", path)
		}
		connection, dialErr := net.DialTimeout("unix", path, 250*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			return nil, fmt.Errorf("another votingd is already listening on %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}

func rejectSymlinkComponents(path string) error {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	cursor := volume + string(filepath.Separator)
	remaining := strings.TrimPrefix(clean, cursor)
	for _, component := range strings.Split(remaining, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		cursor = filepath.Join(cursor, component)
		info, err := os.Lstat(cursor)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect control socket path component: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("control socket path contains symlink component: %s", cursor)
		}
	}
	return nil
}

func ShutdownServers(ctx context.Context, servers ...*http.Server) error {
	var messages []string
	for _, server := range servers {
		if err := server.Shutdown(ctx); err != nil {
			messages = append(messages, err.Error())
		}
	}
	if len(messages) != 0 {
		return errors.New(strings.Join(messages, "; "))
	}
	return nil
}

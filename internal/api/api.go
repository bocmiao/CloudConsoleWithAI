// Package api serves the local web UI and its JSON API.
//
// The server only listens on the loopback interface. Every API request must
// carry the session cookie issued by /auth (the launcher opens that URL with
// a one-time token) plus an X-Miao header, and a Host header naming the
// loopback address, so other local programs, web pages and DNS-rebinding
// tricks cannot drive it.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/ai"
	"github.com/bocmiao/CloudConsoleWithAI/internal/app"
	"github.com/bocmiao/CloudConsoleWithAI/internal/webui"
)

const cookieName = "miao_session"

// Server is the HTTP handler.
type Server struct {
	app     *app.App
	token   string
	port    int
	version string
	mux     *http.ServeMux
}

// New builds the handler. port is the port the listener is bound to.
func New(a *app.App, token string, port int, version string) *Server {
	s := &Server{app: a, token: token, port: port, version: version, mux: http.NewServeMux()}
	static, _ := fs.Sub(webui.Static, "static")
	s.mux.Handle("GET /", http.FileServerFS(static))
	s.mux.HandleFunc("GET /auth", s.handleAuth)

	api := func(pattern string, h func(w http.ResponseWriter, r *http.Request) (any, error)) {
		s.mux.HandleFunc(pattern, s.guard(h))
	}
	api("GET /api/info", s.info)
	api("GET /api/servers", s.listServers)
	api("POST /api/servers", s.addServer)
	api("DELETE /api/servers/{id}", s.deleteServer)
	api("POST /api/servers/{id}/test", s.testServer)
	api("POST /api/servers/{id}/discover", s.discoverServer)
	api("GET /api/servers/{id}/profile", s.serverProfile)
	api("GET /api/ai/presets", s.presets)
	api("GET /api/settings/ai", s.getAISettings)
	api("PUT /api/settings/ai", s.putAISettings)
	api("POST /api/settings/ai/test", s.testAI)
	api("GET /api/certificates", s.getCertificates)
	api("GET /api/settings/free-command", s.getFreeCommand)
	api("PUT /api/settings/free-command", s.putFreeCommand)
	api("GET /api/settings/tencent", s.getTencent)
	api("PUT /api/settings/tencent", s.putTencent)
	api("DELETE /api/settings/tencent", s.deleteTencent)
	api("POST /api/settings/tencent/test", s.testTencent)
	api("GET /api/tencent/servers", s.tencentServers)
	api("GET /api/servers/{id}/cloud", s.serverCloud)
	api("GET /api/eo/sites", s.eoSites)
	api("GET /api/eo/analytics", s.eoAnalytics)
	api("POST /api/chat", s.chat)
	api("POST /api/chat/stream", s.chatStream)
	api("POST /api/chat/stop", s.chatStop)
	api("GET /api/conversations", s.conversations)
	api("GET /api/conversations/{id}", s.conversation)
	api("DELETE /api/conversations/{id}", s.deleteConversation)
	api("GET /api/plans", s.plans)
	api("GET /api/plans/{id}", s.plan)
	api("POST /api/plans/{id}/execute", s.executePlan)
	api("POST /api/plans/{id}/steps/{idx}/undo", s.undoStep)
	api("POST /api/plans/{id}/undo", s.undoPlan)
	api("GET /api/exec", s.execLogs)
	api("GET /api/exec/{id}", s.execEntry)
	api("POST /api/exec/{id}/rollback", s.rollback)
	api("GET /api/servers/{id}/onepanel", s.getOnePanel)
	api("PUT /api/servers/{id}/onepanel", s.putOnePanel)
	api("POST /api/servers/{id}/onepanel/test", s.testOnePanel)
	api("GET /api/audit", s.audit)
	api("GET /api/usage", s.usage)
	return s
}

func (s *Server) hostAllowed(host string) bool {
	h, p, err := net.SplitHostPort(host)
	if err != nil || p != strconv.Itoa(s.port) {
		return false
	}
	return h == "127.0.0.1" || h == "localhost" || h == "::1"
}

// ServeHTTP rejects requests whose Host is not our loopback address.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.hostAllowed(r.Host) {
		http.Error(w, "forbidden host", http.StatusForbidden)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; script-src 'self' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'")
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(s.token)) != 1 {
		http.Error(w, "链接已失效，请从 Miao Panel 窗口里重新打开。", http.StatusForbidden)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

type apiError struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) guard(h func(w http.ResponseWriter, r *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.token)) != 1 || r.Header.Get("X-Miao") != "1" {
			writeJSON(w, http.StatusUnauthorized, apiError{"登录已失效，请从 Miao Panel 窗口里重新打开页面。"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		v, err := h(w, r)
		if _, ok := v.(streamed); ok && err == nil {
			return // the handler wrote the response itself
		}
		if err != nil {
			var ue *app.UserError
			switch {
			case errors.As(err, &ue):
				writeJSON(w, http.StatusBadRequest, apiError{ue.Msg})
			case errors.Is(err, context.DeadlineExceeded):
				writeJSON(w, http.StatusGatewayTimeout, apiError{"操作超时了，请稍后再试。"})
			default:
				writeJSON(w, http.StatusInternalServerError, apiError{"出错了：" + err.Error()})
			}
			return
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func decode(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return &app.UserError{Msg: "请求格式不对"}
	}
	return nil
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, &app.UserError{Msg: "服务器编号不对"}
	}
	return id, nil
}

func (s *Server) info(_ http.ResponseWriter, _ *http.Request) (any, error) {
	return map[string]any{"version": s.version, "secretsKind": s.app.Secrets.Kind()}, nil
}

func (s *Server) listServers(_ http.ResponseWriter, _ *http.Request) (any, error) {
	return s.app.Store.ListServers()
}

func (s *Server) addServer(_ http.ResponseWriter, r *http.Request) (any, error) {
	var req app.AddServerRequest
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return s.app.AddServer(req)
}

func (s *Server) deleteServer(_ http.ResponseWriter, r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, s.app.DeleteServer(id)
}

func (s *Server) testServer(_ http.ResponseWriter, r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	return s.app.TestConnection(r.Context(), id)
}

func (s *Server) discoverServer(_ http.ResponseWriter, r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	if _, _, err := s.app.Discover(r.Context(), id, nil); err != nil {
		return nil, err
	}
	return s.app.Profile(id)
}

func (s *Server) serverProfile(_ http.ResponseWriter, r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	return s.app.Profile(id)
}

func (s *Server) presets(_ http.ResponseWriter, _ *http.Request) (any, error) {
	return ai.Presets, nil
}

func (s *Server) getAISettings(_ http.ResponseWriter, _ *http.Request) (any, error) {
	return s.app.AISettings()
}

func (s *Server) putAISettings(_ http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		app.AISettings
		APIKey string `json:"apiKey"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return s.app.SaveAISettings(req.AISettings, req.APIKey)
}

func (s *Server) testAI(_ http.ResponseWriter, r *http.Request) (any, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	text, err := s.app.TestAI(ctx)
	return map[string]string{"reply": text}, err
}

// getCertificates answers at once with the last overview (refreshing it in
// the background when old); wait=1 waits for a current one and refresh=1
// gathers a new one.
func (s *Server) getCertificates(_ http.ResponseWriter, r *http.Request) (any, error) {
	q := r.URL.Query()
	if q.Get("refresh") == "1" || q.Get("wait") == "1" {
		return s.app.Certificates(r.Context(), q.Get("refresh") == "1")
	}
	return s.app.LatestCertificates(r.Context())
}

func (s *Server) getFreeCommand(_ http.ResponseWriter, _ *http.Request) (any, error) {
	return s.app.FreeCommand()
}

func (s *Server) putFreeCommand(_ http.ResponseWriter, r *http.Request) (any, error) {
	var req app.FreeCommandSettings
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return s.app.SaveFreeCommand(req)
}

func (s *Server) getTencent(_ http.ResponseWriter, _ *http.Request) (any, error) {
	return s.app.Tencent(), nil
}

func (s *Server) putTencent(_ http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		SecretID  string `json:"secretId"`
		SecretKey string `json:"secretKey"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return s.app.SaveTencent(req.SecretID, req.SecretKey)
}

func (s *Server) deleteTencent(_ http.ResponseWriter, _ *http.Request) (any, error) {
	return s.app.ClearTencent(), nil
}

func (s *Server) testTencent(_ http.ResponseWriter, r *http.Request) (any, error) {
	info, err := s.app.TestTencent(r.Context())
	return map[string]string{"info": info}, err
}

func (s *Server) tencentServers(_ http.ResponseWriter, r *http.Request) (any, error) {
	return s.app.TencentServers(r.Context(), r.URL.Query().Get("refresh") == "1")
}

func (s *Server) serverCloud(_ http.ResponseWriter, r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	cs, err := s.app.ServerCloud(r.Context(), id)
	if err != nil {
		// Cloud details are extra information: an error must not break the
		// server page.
		return map[string]string{"error": err.Error()}, nil
	}
	return map[string]any{"instance": cs}, nil
}

func (s *Server) eoSites(_ http.ResponseWriter, r *http.Request) (any, error) {
	return s.app.EOSites(r.Context())
}

func (s *Server) eoAnalytics(_ http.ResponseWriter, r *http.Request) (any, error) {
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	return s.app.EOAnalytics(r.Context(), r.URL.Query().Get("domain"), hours, r.URL.Query().Get("refresh") == "1")
}

func (s *Server) chat(_ http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		ConversationID string `json:"conversationId"`
		Message        string `json:"message"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	return s.app.Chat(ctx, req.ConversationID, req.Message)
}

// streamed is returned by handlers that wrote their response as it went.
type streamed struct{}

// chatStream answers like chat, but as newline-delimited JSON events while
// the AI works: "start", then text, thinking, tool steps and so on (see
// ai.Event), and finally "done" with the whole reply.
func (s *Server) chatStream(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		ConversationID string `json:"conversationId"`
		Message        string `json:"message"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	rc := http.NewResponseController(w)
	var mu sync.Mutex
	started := false
	send := func(v any) {
		mu.Lock()
		defer mu.Unlock()
		if !started {
			started = true
			w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
		}
		data, _ := json.Marshal(v)
		_, _ = w.Write(append(data, '\n'))
		_ = rc.Flush()
	}
	reply, err := s.app.ChatStream(ctx, req.ConversationID, req.Message, func(e app.ChatEvent) { send(e) })
	if err != nil && !started {
		return nil, err // nothing sent yet: an ordinary error response
	}
	if err != nil {
		reply.Error = "出错了：" + err.Error()
	}
	send(map[string]any{"type": "done", "reply": reply})
	return streamed{}, nil
}

func (s *Server) chatStop(_ http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		ConversationID string `json:"conversationId"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return map[string]bool{"stopped": s.app.StopChat(req.ConversationID)}, nil
}

func (s *Server) conversations(_ http.ResponseWriter, _ *http.Request) (any, error) {
	return s.app.Conversations()
}

func (s *Server) conversation(_ http.ResponseWriter, r *http.Request) (any, error) {
	return s.app.Conversation(r.PathValue("id"))
}

func (s *Server) deleteConversation(_ http.ResponseWriter, r *http.Request) (any, error) {
	return map[string]bool{"ok": true}, s.app.DeleteConversation(r.PathValue("id"))
}

func (s *Server) plans(_ http.ResponseWriter, _ *http.Request) (any, error) {
	return s.app.Plans(100)
}

func (s *Server) plan(_ http.ResponseWriter, r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	return s.app.Plan(id)
}

func (s *Server) executePlan(_ http.ResponseWriter, r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	var req struct {
		Steps []int `json:"steps"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return s.app.ExecutePlan(id, req.Steps)
}

func (s *Server) undoStep(_ http.ResponseWriter, r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	idx, err := strconv.Atoi(r.PathValue("idx"))
	if err != nil {
		return nil, &app.UserError{Msg: "步骤编号不对"}
	}
	return s.app.UndoStep(r.Context(), id, idx)
}

func (s *Server) undoPlan(_ http.ResponseWriter, r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	return s.app.UndoPlan(r.Context(), id)
}

func (s *Server) execLogs(_ http.ResponseWriter, r *http.Request) (any, error) {
	return s.app.ExecLogs(r.URL.Query().Get("changes") == "1")
}

func (s *Server) execEntry(_ http.ResponseWriter, r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	return s.app.ExecEntry(id)
}

func (s *Server) rollback(_ http.ResponseWriter, r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	return s.app.Rollback(r.Context(), id)
}

func (s *Server) getOnePanel(_ http.ResponseWriter, r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	return s.app.OnePanel(id)
}

func (s *Server) putOnePanel(_ http.ResponseWriter, r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	var req struct {
		app.OnePanelSettings
		APIKey string `json:"apiKey"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	return s.app.SaveOnePanel(id, req.OnePanelSettings, req.APIKey)
}

func (s *Server) testOnePanel(_ http.ResponseWriter, r *http.Request) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	info, err := s.app.TestOnePanel(r.Context(), id)
	return map[string]string{"info": info}, err
}

func (s *Server) audit(_ http.ResponseWriter, _ *http.Request) (any, error) {
	return s.app.Store.ListAudit(200)
}

func (s *Server) usage(_ http.ResponseWriter, _ *http.Request) (any, error) {
	return s.app.Store.MonthCost()
}

// LaunchURL is the address that logs the browser in for this run.
func LaunchURL(port int, token string) string {
	return "http://127.0.0.1:" + strconv.Itoa(port) + "/auth?token=" + strings.TrimSpace(token)
}

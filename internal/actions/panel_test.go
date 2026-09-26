package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bocmiao/CloudConsoleWithAI/internal/onepanel"
)

// fakeApps is a 1Panel with two installed apps (halo and mysql) that keeps
// the container settings it is sent, and finishes backups on the second look.
type fakeApps struct {
	mu        sync.Mutex
	memory    float64
	unit      string
	specifyIP string
	breakIP   bool // simulate a panel that changes the port binding
	compose   string
	requests  []string
	backups   map[string]int // taskID -> times looked up
}

func (f *fakeApps) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.requests = append(f.requests, r.URL.Path)
	reply := func(data string) { _, _ = io.WriteString(w, `{"code":200,"message":"","data":`+data+`}`) }
	switch r.URL.Path {
	case "/api/v2/apps/installed/search":
		reply(`{"total":2,"items":[{"id":7,"name":"halo","appKey":"halo","container":"1Panel-halo-abcd","serviceName":"halo","status":"Running"},
			{"id":8,"name":"mysql","appKey":"mysql","container":"1Panel-mysql-efgh","status":"Running"}]}`)
	case "/api/v2/apps/installed/params/7":
		data, _ := json.Marshal(map[string]any{
			"params": []any{}, "cpuQuota": 0, "memoryLimit": f.memory, "memoryUnit": f.unit,
			"containerName": "1Panel-halo-abcd", "allowPort": true, "specifyIP": f.specifyIP, "restartPolicy": "always",
			"dockerCompose": f.compose,
		})
		reply(string(data))
	case "/api/v2/apps/installed/params/update":
		if body["installId"] != float64(7) || body["advanced"] != true || body["allowPort"] != true || body["restartPolicy"] != "always" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"code":400,"message":"unexpected update"}`)
			return
		}
		f.memory, _ = body["memoryLimit"].(float64)
		if body["editCompose"] == true {
			f.compose, _ = body["dockerCompose"].(string)
		}
		f.unit, _ = body["memoryUnit"].(string)
		if f.breakIP {
			f.specifyIP = "127.0.0.1"
		} else {
			f.specifyIP, _ = body["specifyIP"].(string)
		}
		reply(`null`)
	case "/api/v2/databases/search":
		reply(`{"total":2,"items":[{"name":"halo_db"},{"name":"blog"}]}`)
	case "/api/v2/backups/backup":
		f.backups[body["taskID"].(string)] = 0
		reply(`null`)
	case "/api/v2/backups/record/search":
		var items []string
		for id, n := range f.backups {
			status := "Waiting"
			if n > 0 {
				status = "Success"
			}
			f.backups[id] = n + 1
			items = append(items, fmt.Sprintf(`{"taskID":%q,"status":%q,"fileDir":"database/mysql/mysql/%s","fileName":"x.sql.gz"}`, id, status, body["detailName"]))
		}
		reply(`{"total":1,"items":[` + strings.Join(items, ",") + `]}`)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"code":404,"message":"not found"}`)
	}
}

func panelEnv(t *testing.T, f *fakeApps) *Env {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()
	c := onepanel.New(func(network, _ string) (net.Conn, error) { return net.Dial(network, addr) }, 1, "k", "", "")
	return &Env{OnePanel: c, PollInterval: 10 * time.Millisecond}
}

func TestAppLimitsApplyAndUndo(t *testing.T) {
	f := &fakeApps{unit: "M", specifyIP: "", backups: map[string]int{}}
	env := panelEnv(t, f)
	ctx := context.Background()
	r, err := Resolve("app.limits.set", map[string]any{"app": "1Panel-halo-abcd", "memory_mb": 1024}, "1panel")
	if err != nil {
		t.Fatal(err)
	}
	out := Apply(ctx, env, r, nil)
	if out.Status != StatusDone || f.memory != 1024 || f.unit != "M" {
		t.Fatalf("apply: %+v memory=%v%s", out, f.memory, f.unit)
	}
	if out.Undo["memory_limit"] != "0" || out.Undo["install_id"] != "7" {
		t.Fatalf("undo data = %v", out.Undo)
	}
	if cmds := strings.Join(out.Commands, "\n"); !strings.Contains(cmds, "POST /api/v2/apps/installed/params/update") || strings.Contains(cmds, "1Panel-Token") {
		t.Fatalf("recorded requests = %q", cmds)
	}
	if env.OnePanel.Trace != nil {
		t.Fatal("trace hook left installed")
	}
	undone := Undo(ctx, env, r, out.Undo)
	if undone.Status != StatusUndone || f.memory != 0 {
		t.Fatalf("undo: %+v memory=%v", undone, f.memory)
	}

	// If the panel changes how ports are published, the change is reverted.
	f.breakIP = true
	out = Apply(ctx, env, r, nil)
	if out.Status != StatusRolledBack || f.memory != 0 || !strings.Contains(strings.Join(out.Log, ""), "端口") {
		t.Fatalf("broken panel: %+v memory=%v", out, f.memory)
	}

	if _, err := Resolve("app.limits.set", map[string]any{"app": "halo", "memory_mb": 32}, "1panel"); err == nil {
		t.Fatal("32MB limit accepted")
	}
	if _, err := Resolve("app.limits.set", map[string]any{"app": "halo", "memory_mb": 512}, "linux"); err == nil {
		t.Fatal("app limits should need 1Panel")
	}
	r, _ = Resolve("app.limits.set", map[string]any{"app": "nextcloud", "memory_mb": 512}, "1panel")
	if out := Apply(ctx, env, r, nil); out.Status != StatusRefused || !strings.Contains(strings.Join(out.Log, ""), "halo、mysql") {
		t.Fatalf("unknown app: %+v", out)
	}
}

func TestBackupWaitsForEveryDatabase(t *testing.T) {
	f := &fakeApps{unit: "M", backups: map[string]int{}}
	env := panelEnv(t, f)
	r, err := Resolve("backup.create", map[string]any{"app": "mysql"}, "1panel")
	if err != nil {
		t.Fatal(err)
	}
	out := Apply(context.Background(), env, r, nil)
	log := strings.Join(out.Log, "\n")
	if out.Status != StatusDone || len(f.backups) != 2 || !strings.Contains(log, "halo_db") || !strings.Contains(log, "blog") {
		t.Fatalf("backup: %+v backups=%v", out, f.backups)
	}
	if r.Cap.Reversible {
		t.Fatal("a backup has nothing to undo")
	}
}

const haloCompose = `services:
  halo:
    image: halohub/halo:2.21
    container_name: ${CONTAINER_NAME}
    restart: always
    environment:
      - TZ=Asia/Shanghai
    command:
      - --spring.r2dbc.url=r2dbc:pool:mysql://mysql:3306/halo
  # the database is another 1Panel app
networks:
  1panel-network:
    external: true
`

func TestJavaHeapApplyAndUndo(t *testing.T) {
	f := &fakeApps{unit: "M", compose: haloCompose, backups: map[string]int{}}
	env := panelEnv(t, f)
	ctx := context.Background()
	r, err := Resolve("java.heap.set", map[string]any{"app": "halo", "max_heap_mb": 768}, "1panel")
	if err != nil {
		t.Fatal(err)
	}
	out := Apply(ctx, env, r, nil)
	if out.Status != StatusDone || !strings.Contains(f.compose, "JAVA_TOOL_OPTIONS=-Xmx768m") || !strings.Contains(f.compose, "TZ=Asia/Shanghai") {
		t.Fatalf("apply: %+v\ncompose:\n%s", out, f.compose)
	}
	if !strings.Contains(strings.Join(out.Log, ""), "原来：没有设置") {
		t.Fatalf("log = %v", out.Log)
	}
	// The app was upgraded meanwhile: undo puts back only the Java option.
	f.compose = strings.Replace(f.compose, "halohub/halo:2.21", "halohub/halo:2.22", 1)
	undone := Undo(ctx, env, r, out.Undo)
	if undone.Status != StatusUndone || strings.Contains(f.compose, "JAVA_TOOL_OPTIONS") || !strings.Contains(f.compose, "halo:2.22") ||
		!strings.Contains(f.compose, "TZ=Asia/Shanghai") || !strings.Contains(f.compose, "# the database is another 1Panel app") {
		t.Fatalf("undo: %+v\ncompose:\n%s", undone, f.compose)
	}
	if _, err := Resolve("java.heap.set", map[string]any{"app": "halo", "max_heap_mb": 768}, "linux"); err == nil {
		t.Fatal("java heap should need 1Panel")
	}
}

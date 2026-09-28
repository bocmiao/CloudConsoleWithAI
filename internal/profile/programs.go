package profile

import (
	"path"
	"sort"
	"strconv"
	"strings"
)

// Program is the processes of one program among those using the most
// memory, for the server page's 应用 list.
type Program struct {
	Name  string  `json:"name"`
	Procs int     `json:"procs"`
	MemMB int     `json:"memMB"`
	CPU   float64 `json:"cpu"`
	User  string  `json:"user"`
}

// Services are the systemd services, by name.
type Services struct {
	Running []string `json:"running"`
	Failed  []string `json:"failed"`
}

// Container is one Docker container.
type Container struct {
	Name    string `json:"name"`
	Image   string `json:"image"`
	Status  string `json:"status"`
	Ports   string `json:"ports,omitempty"`
	Running bool   `json:"running"`
	Mem     string `json:"mem,omitempty"` // "610MiB / 1.9GiB"
	CPU     string `json:"cpu,omitempty"`
}

// interpreters run a script whose name says more than theirs.
var interpreters = map[string]bool{"python": true, "python2": true, "python3": true, "node": true, "java": true, "php": true,
	"ruby": true, "perl": true, "bun": true, "deno": true, "bash": true, "sh": true, "dotnet": true}

// progName names the program behind a command line: "php-fpm: pool www"
// is php-fpm, "/usr/sbin/mysqld --x" is mysqld, "node /app/server.js" is
// "node server.js".
func progName(args string) string {
	f := strings.Fields(args)
	if len(f) == 0 {
		return ""
	}
	// Process titles set by the program itself: "nginx: worker process".
	if head, _, ok := strings.Cut(args, ": "); ok && !strings.ContainsAny(head, " \t") {
		return path.Base(head)
	}
	base := path.Base(f[0])
	if !interpreters[strings.TrimRight(base, "0123456789.")] {
		return base
	}
	for i := 1; i < len(f); i++ {
		a := f[i]
		if strings.HasPrefix(a, "-") {
			// "java -cp x.jar Main": the value belongs to the flag.
			if a == "-cp" || a == "-classpath" {
				i++
			}
			continue
		}
		return base + " " + path.Base(a)
	}
	return base
}

func (p *Profile) parsePrograms() {
	byName := map[string]*Program{}
	for _, l := range p.sections["procs_by_mem"] {
		// PID PPID USER RSS %CPU ELAPSED COMMAND...
		f := strings.Fields(l)
		if len(f) < 7 || f[0] == "PID" {
			continue
		}
		rss, err := strconv.Atoi(f[3])
		if err != nil {
			continue
		}
		cpu, _ := strconv.ParseFloat(f[4], 64)
		name := progName(strings.Join(f[6:], " "))
		if name == "" {
			continue
		}
		g := byName[name]
		if g == nil {
			g = &Program{Name: name, User: f[2]}
			byName[name] = g
		}
		g.Procs++
		g.MemMB += rss / 1024
		g.CPU += cpu
	}
	for _, g := range byName {
		p.Programs = append(p.Programs, *g)
	}
	sort.Slice(p.Programs, func(i, j int) bool {
		if p.Programs[i].MemMB != p.Programs[j].MemMB {
			return p.Programs[i].MemMB > p.Programs[j].MemMB
		}
		return p.Programs[i].Name < p.Programs[j].Name
	})
}

func (p *Profile) parseServices() {
	p.Services.Running = strings.Fields(p.value("services", "running"))
	for _, s := range strings.Fields(p.value("services", "failed")) {
		p.Services.Failed = append(p.Services.Failed, strings.TrimSuffix(s, ".service"))
	}
}

func (p *Profile) parseContainers() {
	for _, l := range p.block("docker", "containers") {
		f := strings.SplitN(l, "|", 4)
		if len(f) < 3 {
			continue
		}
		c := Container{Name: f[0], Image: f[1], Status: f[2], Running: strings.HasPrefix(f[2], "Up")}
		if len(f) == 4 {
			c.Ports = f[3]
		}
		p.Docker.List = append(p.Docker.List, c)
	}
	for _, l := range p.block("docker", "stats") {
		f := strings.Split(l, "|")
		if len(f) < 2 {
			continue
		}
		for i := range p.Docker.List {
			c := &p.Docker.List[i]
			if c.Name != f[0] {
				continue
			}
			for _, kv := range f[1:] {
				if v, ok := strings.CutPrefix(kv, "mem="); ok {
					c.Mem = v
				} else if v, ok := strings.CutPrefix(kv, "cpu="); ok {
					c.CPU = v
				}
			}
		}
	}
}

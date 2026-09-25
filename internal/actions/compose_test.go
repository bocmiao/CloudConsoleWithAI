package actions

import (
	"strings"
	"testing"
)

func TestSetComposeEnv(t *testing.T) {
	set := func(v string) func(string) string { return func(string) string { return v } }
	for _, c := range []struct {
		name, compose, service, want, keep string
	}{
		{"list form", "services:\n  app:\n    image: x\n    environment:\n      - A=1\n      - JAVA_TOOL_OPTIONS=-Xmx1g\n", "app", "JAVA_TOOL_OPTIONS=NEW", "A=1"},
		{"map form", "services:\n  app:\n    image: x\n    environment:\n      A: \"1\"\n", "app", "JAVA_TOOL_OPTIONS: NEW", "A: \"1\""},
		{"no environment", "services:\n  app:\n    image: x\n", "", "JAVA_TOOL_OPTIONS: NEW", "image: x"},
		{"empty environment", "services:\n  app:\n    image: x\n    environment:\n", "app", "JAVA_TOOL_OPTIONS: NEW", "image: x"},
		{"named among several", "services:\n  db:\n    image: mysql\n  app:\n    image: x\n", "app", "JAVA_TOOL_OPTIONS: NEW", "image: mysql"},
	} {
		got, err := setComposeEnv(c.compose, c.service, "JAVA_TOOL_OPTIONS", set("NEW"))
		if err != nil || !strings.Contains(got, c.want) || !strings.Contains(got, c.keep) {
			t.Errorf("%s: err=%v\n%s", c.name, err, got)
		}
	}
	two := "services:\n  db:\n    image: mysql\n  app:\n    image: x\n"
	if got, _ := setComposeEnv(two, "app", "K", set("V")); strings.Count(got, "K: V") != 1 || strings.Index(got, "K: V") < strings.Index(got, "app:") {
		t.Errorf("variable set on the wrong service:\n%s", got)
	}
	if _, err := setComposeEnv(two, "web", "K", set("V")); err == nil {
		t.Error("unknown service among several should fail")
	}
	if _, err := setComposeEnv("not: [valid", "", "K", set("V")); err == nil {
		t.Error("broken YAML should fail")
	}
}

func TestWithMaxHeap(t *testing.T) {
	for in, want := range map[string]string{
		"":       "-Xmx768m",
		"-Xmx1g": "-Xmx768m",
		"-Dfile.encoding=UTF-8 -XX:MaxRAMPercentage=50": "-Dfile.encoding=UTF-8 -Xmx768m",
		"-Xms256m -XX:+UseG1GC":                         "-Xms256m -XX:+UseG1GC -Xmx768m",
	} {
		if got := withMaxHeap(in, 768); got != want {
			t.Errorf("withMaxHeap(%q) = %q, want %q", in, got, want)
		}
	}
}

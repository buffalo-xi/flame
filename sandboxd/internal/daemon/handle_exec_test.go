package daemon

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// 单工的两条启动路径：直传（argv 原样）与语言路径（解释器由 sandboxd 按 mode 解析），
// 以及它们的互斥约束 —— 这两条路径的分界就是「解释器入口的认知归谁」。
func TestBuildExecArgv(t *testing.T) {
	resolve := func(lang string) (string, error) {
		if lang == "lua" {
			return "/usr/local/bin/lua", nil
		}
		return "", fmt.Errorf("%w: %q", ErrUnsupportedLang, lang)
	}

	t.Run("直传：argv 原样返回", func(t *testing.T) {
		got, err := buildExecArgv("", []string{"sh", "-c", "echo hi"}, "", nil, resolve)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Join(got, " ") != "sh -c echo hi" {
			t.Errorf("argv = %v", got)
		}
	})

	t.Run("直传：空 argv 报错", func(t *testing.T) {
		if _, err := buildExecArgv("", nil, "", nil, resolve); err == nil {
			t.Error("want error for empty argv, got nil")
		}
	})

	t.Run("语言路径：拼成 解释器+脚本+参数", func(t *testing.T) {
		got, err := buildExecArgv("lua", nil, "/skill/scripts/main.lua", []string{"a", "b"}, resolve)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "/usr/local/bin/lua /skill/scripts/main.lua a b"
		if strings.Join(got, " ") != want {
			t.Errorf("argv = %q, want %q", strings.Join(got, " "), want)
		}
	})

	t.Run("语言路径：lang 与 argv 互斥", func(t *testing.T) {
		if _, err := buildExecArgv("lua", []string{"lua", "/x.lua"}, "/x.lua", nil, resolve); err == nil {
			t.Error("want error when both lang and argv are set, got nil（会拼出 lua lua /x.lua）")
		}
	})

	t.Run("语言路径：缺脚本路径报错", func(t *testing.T) {
		if _, err := buildExecArgv("lua", nil, "", nil, resolve); err == nil {
			t.Error("want error when script is empty, got nil")
		}
	})

	t.Run("语言路径：未定义语言保留哨兵错误", func(t *testing.T) {
		_, err := buildExecArgv("ruby", nil, "/x.rb", nil, resolve)
		if !errors.Is(err, ErrUnsupportedLang) {
			t.Errorf("err = %v, want ErrUnsupportedLang（驱动协议错误码 unsupported_lang）", err)
		}
	})
}

// 未定义语言 / 未按当前 mode 配置命令，都必须报错（启动期与请求期行为一致）。
func TestResolveLangCommand(t *testing.T) {
	cfg := &Config{
		Mode: "docker",
		Langs: map[string]LangDef{
			"lua":  {Command: "lua"},
			"ruby": {CommandByMode: map[string]string{"none": "ruby.exe"}},
		},
	}
	if got, err := cfg.ResolveLangCommand("lua"); err != nil || got != "lua" {
		t.Errorf("lua = %q, %v; want lua, nil", got, err)
	}
	if _, err := cfg.ResolveLangCommand("go"); !errors.Is(err, ErrUnsupportedLang) {
		t.Errorf("未定义语言应返回 ErrUnsupportedLang, got %v", err)
	}
	// 定义了但当前 mode 解析不到 → 不是 unsupported_lang，而是配置缺项
	if _, err := cfg.ResolveLangCommand("ruby"); err == nil {
		t.Error("want error for mode without matching command, got nil")
	}
}

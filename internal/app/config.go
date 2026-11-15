package app

import (
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"
)

type Settings struct {
	Invites  bool   `json:"invites"`
	Group    string `json:"group"`
	AI       bool   `json:"ai"`
	Name     string `json:"name"`
	Aliases  string `json:"aliases"`
	Endpoint string `json:"endpoint"`
	Model    string `json:"model"`
	Persona  string `json:"persona"`
	Mention  bool   `json:"mention"`
	Reply    bool   `json:"reply"`
	Named    bool   `json:"named"`
	Turns    int    `json:"turns"`
	Rate     int    `json:"rate"`
	Output   int    `json:"output"`
	Budget   int    `json:"budget"`
	Channels bool   `json:"channels"`
	Interval int    `json:"interval"`
	Keywords string `json:"keywords"`
	Cooldown int    `json:"cooldown"`
}

func defaults() Settings {
	return Settings{Group: "TokenRouter 交流群", Name: "小路", Aliases: "小路", Endpoint: "https://api.openai.com/v1", Model: "", Persona: "你是社群助手小路。友好、简洁地帮助用户了解产品和模型使用。账号与账单问题引导用户联系网站支持。", Mention: true, Reply: true, Named: true, Turns: 10, Rate: 6, Output: 1024, Budget: 100000, Interval: 60, Keywords: "渠道状态,状态", Cooldown: 60}
}

func words(value string) []string {
	var result []string
	for _, word := range strings.FieldsFunc(value, func(character rune) bool { return character == ',' || character == '，' || character == '\n' }) {
		if word = strings.TrimSpace(word); word != "" {
			result = append(result, word)
		}
	}
	return result
}

func (settings Settings) validate() error {
	endpoint, err := url.Parse(settings.Endpoint)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || !(endpoint.Scheme == "https" || endpoint.Scheme == "http" && (endpoint.Hostname() == "localhost" || endpoint.Hostname() == "127.0.0.1" || endpoint.Hostname() == "::1")) {
		return errors.New("模型接口须使用 HTTPS（仅本机地址允许 HTTP），不可包含凭据、查询或片段")
	}
	if strings.TrimSpace(settings.Group) == "" || utf8.RuneCountInString(settings.Group) > 40 || strings.TrimSpace(settings.Name) == "" || utf8.RuneCountInString(settings.Name) > 24 || len(settings.Aliases) > 240 || len(settings.Model) > 100 || len(settings.Persona) > 8000 || strings.TrimSpace(settings.Persona) == "" || settings.AI && strings.TrimSpace(settings.Model) == "" {
		return errors.New("请检查群名、助手名称、模型和人设")
	}
	if !settings.Mention && !settings.Reply && !settings.Named || settings.Named && len(words(settings.Aliases)) == 0 {
		return errors.New("至少选择一种触发方式，别名触发需填写别名")
	}
	if settings.Turns < 1 || settings.Turns > 30 || settings.Rate < 1 || settings.Rate > 30 || settings.Output < 128 || settings.Output > 4096 || settings.Budget < 1000 || settings.Budget > 10000000 {
		return errors.New("AI 用量限制超出范围")
	}
	if settings.Interval != 0 && settings.Interval != 30 && settings.Interval != 60 && settings.Interval != 120 || len(words(settings.Keywords)) == 0 || len(settings.Keywords) > 400 || settings.Cooldown < 10 || settings.Cooldown > 3600 {
		return errors.New("请检查播报频率、关键词及冷却时间（10–3600 秒）")
	}
	return nil
}

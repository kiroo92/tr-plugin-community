package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type ChatTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type Usage struct {
	Day    string `json:"day"`
	Tokens int64  `json:"tokens"`
	Calls  int64  `json:"calls"`
}
type Rate struct {
	Minute int64 `json:"minute"`
	Count  int   `json:"count"`
}

func (plugin *Plugin) handleMessage(ctx context.Context, message *Message) error {
	text := strings.TrimSpace(message.Text)
	if text == "" || message.From.ID <= 0 {
		return nil
	}
	if plugin.settings.Channels {
		for _, keyword := range words(plugin.settings.Keywords) {
			if text == keyword {
				return plugin.broadcast(ctx, true)
			}
		}
	}
	if !plugin.settings.AI {
		return nil
	}
	var bot Bot
	if _, err := plugin.load(ctx, plugin.scoped("bot"), &bot); err != nil {
		return err
	}
	triggered := plugin.settings.Mention && bot.Username != "" && strings.Contains(strings.ToLower(text), "@"+strings.ToLower(bot.Username)) || plugin.settings.Reply && message.Reply != nil && message.Reply.From.ID == bot.ID
	if plugin.settings.Named {
		for _, alias := range words(plugin.settings.Aliases) {
			if strings.HasPrefix(text, alias) {
				triggered = true
			}
		}
	}
	if !triggered {
		return nil
	}
	if utf8.RuneCountInString(text) > 1000 {
		return errors.New("AI 单条问题超过 1000 字符，已跳过")
	}
	if plugin.key == "" {
		return errors.New("未配置模型 API Key")
	}
	rateKey := plugin.scoped("rate/" + strconv.FormatInt(message.From.ID, 10))
	var rate Rate
	if _, err := plugin.load(ctx, rateKey, &rate); err != nil {
		return err
	}
	minute := plugin.now().Unix() / 60
	if rate.Minute != minute {
		rate = Rate{Minute: minute}
	}
	if rate.Count >= plugin.settings.Rate {
		return nil
	}
	rate.Count++
	if err := plugin.save(ctx, rateKey, rate); err != nil {
		return err
	}
	return plugin.answer(ctx, message)
}

func (plugin *Plugin) usage(ctx context.Context) (Usage, error) {
	var usage Usage
	_, err := plugin.load(ctx, plugin.scoped("usage"), &usage)
	day := plugin.now().In(time.FixedZone("UTC+8", 8*3600)).Format("2006-01-02")
	if usage.Day != day {
		usage = Usage{Day: day}
	}
	return usage, err
}

func (plugin *Plugin) answer(ctx context.Context, message *Message) error {
	historyKey := plugin.scoped("history/" + strconv.FormatInt(message.From.ID, 10))
	var history []ChatTurn
	if _, err := plugin.load(ctx, historyKey, &history); err != nil {
		return err
	}
	if len(history) > plugin.settings.Turns*2 {
		history = history[len(history)-plugin.settings.Turns*2:]
	}
	history = append(history, ChatTurn{Role: "user", Content: strings.TrimSpace(message.Text)})
	inputSize := func() int {
		size := len(plugin.settings.Persona) + 128
		for _, turn := range history {
			size += len(turn.Content) + 32
		}
		return size
	}
	for inputSize() > 24000 && len(history) > 1 {
		history = history[2:]
	}
	reservation := int64(inputSize() + plugin.settings.Output)
	usage, err := plugin.usage(ctx)
	if err != nil {
		return err
	}
	if usage.Tokens+reservation > int64(plugin.settings.Budget) {
		return errors.New("群级每日预算不足，已跳过模型调用")
	}
	usage.Tokens += reservation
	if err := plugin.save(ctx, plugin.scoped("usage"), usage); err != nil {
		return err
	}
	messages := append([]ChatTurn{{Role: "system", Content: plugin.settings.Persona}}, history...)
	body, err := json.Marshal(map[string]any{"model": plugin.settings.Model, "messages": messages, "max_tokens": plugin.settings.Output, "stream": false})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(plugin.settings.Endpoint, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return errors.New("模型接口地址无效")
	}
	request.Header.Set("Authorization", "Bearer "+plugin.key)
	request.Header.Set("Content-Type", "application/json")
	response, err := plugin.client.Do(request)
	if err != nil {
		return errors.New("模型请求失败，已保留预留预算")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return errors.New("模型接口返回错误，已保留预留预算，请检查模型配置")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 128<<10+1))
	if err != nil || len(raw) > 128<<10 {
		return errors.New("模型响应过大或不可读")
	}
	var result struct {
		Choices []struct {
			Message ChatTurn `json:"message"`
		} `json:"choices"`
		Usage struct {
			Total int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &result) != nil || len(result.Choices) == 0 || strings.TrimSpace(result.Choices[0].Message.Content) == "" {
		return errors.New("模型返回了空回复或不支持的响应格式")
	}
	if result.Usage.Total > 0 {
		usage.Tokens += result.Usage.Total - reservation
	}
	usage.Calls++
	if err := plugin.save(ctx, plugin.scoped("usage"), usage); err != nil {
		return err
	}
	answer := []rune(result.Choices[0].Message.Content)
	if len(answer) > 4000 {
		answer = answer[:4000]
	}
	reply := string(answer)
	history = append(history, ChatTurn{Role: "assistant", Content: reply})
	if len(history) > plugin.settings.Turns*2 {
		history = history[len(history)-plugin.settings.Turns*2:]
	}
	if err := plugin.save(ctx, historyKey, history); err != nil {
		return err
	}
	if err := plugin.telegram(ctx, "sendMessage", map[string]any{"chat_id": plugin.chat, "text": reply, "reply_parameters": map[string]any{"message_id": message.ID, "allow_sending_without_reply": true}}, nil); err != nil {
		return err
	}
	return plugin.event(ctx, "AI 助手已回复", "群内主动触发对话；不记录消息正文。")
}

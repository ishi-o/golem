// Package i18n resolves the runtime's own messages, separate from model
// output. It embeds the built-in bundles and exposes Bundle, which an
// An application can wrap or extend it by constructing one over more maps.
//
// Only these strings go through a bundle. Model output is whatever language
// the user wrote in, and tool results are data.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

//go:embed messages.json messages_zh_CN.json

var files embed.FS

// Keys the runtime itself looks up. Integrations may add their own keys to a
// Bundle built over extended maps.
const (
	// QuestionAsked is the model-facing instruction returned in place of a
	// real answer when the questions are now in front of the user.
	QuestionAsked = "question-asked"
	// QuestionAlreadyAsked is the model-facing instruction when an
	// unanswered ask is already outstanding in the conversation.
	QuestionAlreadyAsked = "question-already-asked"
	// QuestionCannotAsk is the model-facing instruction when no surface
	// could present the questions at all.
	QuestionCannotAsk = "question-cannot-ask"
)

// Bundle resolves keys to localized strings. The zero value resolves nothing;
// use New.
type Bundle struct {
	// locale is the tag the bundle was built for ("en", "zh-CN").
	locale string
	maps   map[string]map[string]string
	log    *slog.Logger
}

// New loads every embedded bundle (base plus locales) and selects locale.
// The base file is the fallback for a key the locale has no entry for, so a
// partially translated locale renders rather than misses. An unknown locale
// falls back to the base language too — a missing translation is a bug, but
// a crash over it is a bigger one.
func New(locale string, log *slog.Logger) *Bundle {
	if log == nil {
		log = slog.Default()
	}
	maps := map[string]map[string]string{}
	for _, name := range []string{"messages.json", "messages_zh_CN.json"} {
		data, err := files.ReadFile(name)
		if err != nil {
			// An embedded file cannot go missing without a build change, so
			// this is a packaging bug; say so loudly and carry on with the
			// rest.
			log.Error("i18n bundle unreadable", "file", name, "err", err)
			continue
		}
		m := map[string]string{}
		if err := json.Unmarshal(data, &m); err != nil {
			log.Error("i18n bundle unparseable", "file", name, "err", err)
			continue
		}
		maps[strings.TrimSuffix(strings.TrimPrefix(name, "messages"), ".json")] = m
	}
	// "zh-CN" names the same bundle the file suffix "zh_CN" does.
	if locale != "" {
		locale = strings.ReplaceAll(locale, "-", "_")
	}
	return &Bundle{locale: locale, maps: maps, log: log}
}

// Get resolves a key with optional format args. A missing key returns the
// key itself and logs: the string still identifies itself in the output, and
// the log line is what gets the translation fixed.
func (b *Bundle) Get(key string, args ...any) string {
	for _, candidate := range b.keys() {
		if m, ok := b.maps[candidate]; ok {
			if s, ok := m[key]; ok {
				if len(args) == 0 {
					return s
				}
				return fmt.Sprintf(s, args...)
			}
		}
	}
	b.log.Warn("i18n key missing", "key", key, "locale", b.locale)
	return key
}

// keys lists the bundle names to try: the selected locale first, then the
// base.
func (b *Bundle) keys() []string {
	if b.locale == "" || b.locale == "en" {
		return []string{"", "zh_CN"}
	}
	return []string{b.locale, ""}
}

// Extend returns a bundle that resolves keys from extra first, then the
// embedded bundles — the mechanism an embedding application uses to add its
// own strings in its own locale files.
func (b *Bundle) Extend(extra map[string]map[string]string) *Bundle {
	merged := map[string]map[string]string{}
	for k, v := range b.maps {
		merged[k] = v
	}
	for k, v := range extra {
		if existing, ok := merged[k]; ok {
			for key, val := range v {
				existing[key] = val
			}
		} else {
			merged[k] = v
		}
	}
	return &Bundle{locale: b.locale, maps: merged, log: b.log}
}

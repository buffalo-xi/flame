package core

import (
	"context"
	"regexp"
	"strings"
)

// MetaLanguageKey is the key carrying the caller's desired output language inside params._meta.
//
// It sits in _meta for the same reason session_id and progressToken do: it describes **how** this call should be
// handled, not what the business input is. Nothing about it is endpoint-specific, so a caller can set it once for
// every endpoint it invokes, and no tenant-defined argument schema grows a field that the caller's model would try
// to fill with something else.
const MetaLanguageKey = "language"

// OutputLanguageKey is the reserved argument key the entry points use to carry the hint from _meta down to prompt
// assembly - the same pass-through shape as session_id. The "_" prefix keeps it out of the tenant's namespace.
const OutputLanguageKey = "_output_language"

// maxLanguageTagLen bounds what may end up in the system prompt. The hint is caller-supplied, so it is validated as
// a language tag and never interpolated as free text.
const maxLanguageTagLen = 35

var languageTagPattern = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{1,8})*$`)

// languageLabels turns a tag into a name the model cannot misread. A tag missing here is passed through as itself:
// still valid, just less explicit. Keys are lower case.
var languageLabels = map[string]string{
	"zh":      "Chinese",
	"zh-cn":   "Simplified Chinese",
	"zh-hans": "Simplified Chinese",
	"zh-tw":   "Traditional Chinese",
	"zh-hant": "Traditional Chinese",
	"en":      "English",
	"ja":      "Japanese",
	"ko":      "Korean",
	"fr":      "French",
	"de":      "German",
	"es":      "Spanish",
	"pt":      "Portuguese",
	"ru":      "Russian",
	"ar":      "Arabic",
	"it":      "Italian",
}

// NormalizeOutputLanguage validates a caller-supplied language tag and returns the tag plus a readable label.
//
// A malformed or unknown value returns empty strings, i.e. "not given", instead of an error: _meta is advisory, and
// failing a whole call because a caller mistyped a tag would be worse than ignoring the hint.
func NormalizeOutputLanguage(raw string) (tag, label string) {
	tag = strings.TrimSpace(raw)
	if tag == "" || len(tag) > maxLanguageTagLen || !languageTagPattern.MatchString(tag) {
		return "", ""
	}
	if l, ok := languageLabels[strings.ToLower(tag)]; ok {
		return tag, l
	}
	return tag, tag
}

// OutputLanguageFromMeta reads params._meta.language (the same location for MCP and JSON-RPC).
//
// When params is not an object, or _meta is not an object, it returns "" = "not given".
func OutputLanguageFromMeta(params any) string {
	mp, ok := params.(map[string]any)
	if !ok {
		return ""
	}
	meta, ok := mp["_meta"].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := meta[MetaLanguageKey].(string)
	return s
}

// WithOutputLanguage returns a copy of args carrying the reserved hint (the original map is not modified).
//
// Entry points use this when the hint arrives in _meta but prompt assembly only reads the arguments.
func WithOutputLanguage(args map[string]any, tag string) map[string]any {
	if tag == "" {
		return args
	}
	merged := make(map[string]any, len(args)+1)
	for k, v := range args {
		merged[k] = v
	}
	merged[OutputLanguageKey] = tag
	return merged
}

// OutputLanguage returns the validated hint carried in args.
//
// It validates again on purpose: an internal caller (an agent calling another agent) may set the key directly, and the
// value still ends up in a system prompt.
func OutputLanguage(args map[string]any) (tag, label string) {
	if args == nil {
		return "", ""
	}
	raw, _ := args[OutputLanguageKey].(string)
	return NormalizeOutputLanguage(raw)
}

// languageHintKey is the ctx key carrying the hint for in-process call chains (the same shape as callchain.With).
type languageHintKey struct{}

// languageHint is the ctx value: the tag plus whether a caller pinned it.
//
// The pin decides one thing only - whether the value travels **out of the process** (params._meta.language). In-process
// the tag alone is enough: a flow exposes it as the builtin variable `language`, and only a pinned value is re-sent to
// whatever that flow calls next.
type languageHint struct {
	tag    string
	pinned bool
}

// WithLanguageHint records the language this run is being carried out in, without pinning it.
//
// ctx is how a chain keeps the intent without threading a parameter through every layer: an agent that was told "answer in
// zh-CN" hands the same tag to whatever it calls next, so a three-hop chain does not lose it. A value recorded here is
// informational: it fills a flow's builtin `language` but must never override a callee's own default (which is what
// sending it as params._meta.language would do).
func WithLanguageHint(ctx context.Context, tag string) context.Context {
	if tag == "" {
		return ctx
	}
	return context.WithValue(ctx, languageHintKey{}, languageHint{tag: tag})
}

// WithPinnedLanguage is WithLanguageHint for a value a caller demanded: it also travels to callees over the wire.
func WithPinnedLanguage(ctx context.Context, tag string) context.Context {
	if tag == "" {
		return ctx
	}
	return context.WithValue(ctx, languageHintKey{}, languageHint{tag: tag, pinned: true})
}

// LanguageHintFrom reads the tag recorded by WithLanguageHint / WithPinnedLanguage. Empty means "not given".
func LanguageHintFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(languageHintKey{}).(languageHint)
	return v.tag
}

// LanguagePinnedFrom reports whether the tag was pinned by a caller, as opposed to resolved from a default.
func LanguagePinnedFrom(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(languageHintKey{}).(languageHint)
	return v.pinned
}

// StripOutputLanguage returns a copy of args without the reserved key (the original map is not modified).
//
// The hint is control intent: it must not stay in the business arguments, where it would show up in the model's view of
// the input (or collide with a tenant field) while already having done its job in the system prompt.
func StripOutputLanguage(args map[string]any) map[string]any {
	if args == nil {
		return nil
	}
	if _, ok := args[OutputLanguageKey]; !ok {
		return args
	}
	clean := make(map[string]any, len(args))
	for k, v := range args {
		if k == OutputLanguageKey {
			continue
		}
		clean[k] = v
	}
	return clean
}

package exportgate

import (
	"fmt"
	"html"
	"net/url"
	"path"
	"regexp"
	"strings"
	"testing"
)

var (
	inlineLink     = regexp.MustCompile(`\[(?:\\.|[^\[\]\\]|\[[^\]\n]*\])*\]\(\s*(<[^<>\n]*>|(?:\\.|[^\s()\\]|\((?:\\.|[^()\\])*\))+)\s*(?:"[^"\n]*"|'[^'\n]*'|\([^()\n]*\))?\)`)
	referenceLink  = regexp.MustCompile(`(?m)^ {0,3}\[[^\]\n]+\]:\s*(<[^<>\n]*>|\S+)`)
	markdownEscape = regexp.MustCompile(`\\([[:punct:]])`)
)

// Destinations are resolved against archive members, never files that happen to
// be present in the checkout. URL fragments and queries do not name files.
func brokenMarkdownLinks(name, source string, files map[string][]byte) []string {
	source = maskMarkdownCode(source)
	var findings []string
	for _, pattern := range []*regexp.Regexp{inlineLink, referenceLink} {
		for _, match := range pattern.FindAllStringSubmatchIndex(source, -1) {
			if match[0] > 0 && source[match[0]-1] == '\\' {
				continue
			}
			destination := strings.Trim(source[match[2]:match[3]], "<>")
			destination = html.UnescapeString(markdownEscape.ReplaceAllString(destination, "$1"))
			u, err := url.Parse(destination)
			if err != nil {
				findings = append(findings, fmt.Sprintf("%s: invalid Markdown destination %q: %v", name, destination, err))
				continue
			}
			if u.IsAbs() || u.Host != "" || u.Path == "" {
				continue
			}
			target := path.Join(path.Dir(name), u.Path)
			if strings.HasPrefix(u.Path, "/") {
				target = path.Clean(strings.TrimPrefix(u.Path, "/"))
			}
			exists := target == "."
			if _, ok := files[target]; ok {
				exists = true
			}
			// Git archives can omit directory headers; a shipped child proves the directory.
			for member := range files {
				if strings.HasPrefix(member, target+"/") {
					exists = true
					break
				}
			}
			if !exists {
				line := strings.Count(source[:match[0]], "\n") + 1
				findings = append(findings, fmt.Sprintf("%s:%d: broken relative Markdown link %q (archive path %s)", name, line, destination, target))
			}
		}
	}
	return findings
}

func maskMarkdownCode(source string) string {
	data := []byte(source)
	blank := func(start, end int) {
		for i := start; i < end; i++ {
			if data[i] != '\n' {
				data[i] = ' '
			}
		}
	}
	var fence byte
	fenceLength := 0
	for start := 0; start < len(data); {
		end := strings.IndexByte(source[start:], '\n')
		if end < 0 {
			end = len(data)
		} else {
			end += start + 1
		}
		line := strings.TrimSpace(source[start:end])
		count := 0
		if len(line) > 0 && (line[0] == '`' || line[0] == '~') {
			for count < len(line) && line[count] == line[0] {
				count++
			}
		}
		if fence != 0 {
			blank(start, end)
			if count >= fenceLength && line[0] == fence && strings.TrimSpace(line[count:]) == "" {
				fence = 0
			}
		} else if count >= 3 {
			fence = line[0]
			fenceLength = count
			blank(start, end)
		} else if strings.HasPrefix(source[start:end], "    ") || strings.HasPrefix(source[start:end], "\t") {
			blank(start, end)
		}
		start = end
	}
	for i := 0; i < len(data); i++ {
		if data[i] != '`' {
			continue
		}
		count := 1
		for i+count < len(data) && data[i+count] == '`' {
			count++
		}
		for end := i + count; end < len(data); {
			offset := strings.Index(string(data[end:]), strings.Repeat("`", count))
			if offset < 0 {
				break
			}
			end += offset
			if end+count < len(data) && data[end+count] == '`' {
				for end < len(data) && data[end] == '`' {
					end++
				}
				continue
			}
			blank(i, end+count)
			i = end + count - 1
			break
		}
	}
	return string(data)
}

func TestMarkdownLinks(t *testing.T) {
	files := map[string][]byte{"docs/page.md": nil, "README.md": nil, "docs/space name.md": nil, "docs/a(b).md": nil, "images/icon.svg": nil}
	for _, source := range []string{
		"[readme](../README.md#intro)", "[root](/README.md?raw=1)", "[local](#intro)",
		"[site](https://example.com/missing.md)", "[site](//example.com/missing.md)",
		"[mail](mailto:person@example.com)", "[space](space%20name.md)", "[space](<space name.md>)",
		"[parens](a(b).md)", `[parens](a\(b\).md)`, "[directory](../images/)",
		"[ref]: ../README.md \"title\"\n[readme][ref]", "[ref]:\n  ../README.md\n[ref]",
		"`[example](missing.md)`", "`` [example](missing.md) ` ``",
		"```md\n[example](missing.md)\n```", "~~~\n[example](missing.md)\n~~~",
		"    [example](missing.md)", "[label `code`](../README.md)", `[title](../README.md "title")`,
		`\[escaped](absent.md)`, "unopened](absent.md)", "[unclosed](absent.md",
	} {
		if got := brokenMarkdownLinks("docs/page.md", source, files); len(got) != 0 {
			t.Errorf("%q: %v", source, got)
		}
	}
	for _, source := range []string{
		"[missing](absent.md)", "![image](../images/absent.svg)", "[escape](../../README.md)",
		"[ref]: absent.md\n[missing][ref]", "[space](<absent file.md>)", "[query](absent.md?raw=1#intro)",
		"[ref]:\n  absent.md\n[ref]", "[label `code`](absent.md)",
	} {
		if got := brokenMarkdownLinks("docs/page.md", source, files); len(got) != 1 {
			t.Errorf("%q: want one broken link, got %v", source, got)
		}
	}
}

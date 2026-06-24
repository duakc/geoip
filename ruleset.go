package main

import (
	"bufio"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strings"
	"unsafe"

	"go4.org/netipx"
)

var (
	ErrEmpty = fmt.Errorf("empty")
)

type RuleSet interface {
	Count() int64
	WriteSRS(w io.Writer, format string) error
	WriteMRS(w io.Writer, format string, behavior int) error
}

var (
	_ RuleSet = (*DomainRuleset)(nil)
	_ RuleSet = (*IPRuleset)(nil)
)

type FileError struct {
	Reason string
	Line   int
	Raw    string
}

func (fe *FileError) Error() string {
	return fmt.Sprintf("syntax error: %s at line %d: %s", fe.Reason, fe.Line, fe.Raw)
}

type DomainRuleset struct {
	Domain  []string
	Suffix  []string
	KeyWord []string
	Regexp  []string
}

func (rule *DomainRuleset) Count() int64 {
	if rule == nil {
		return 0
	}
	return int64(len(rule.Domain) + len(rule.Suffix) + len(rule.KeyWord) + len(rule.Regexp))
}

func NewDomainFile(path string) (*DomainRuleset, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	domainFile := new(DomainRuleset)
	sc := bufio.NewScanner(file)
	lineCount := -1
	for sc.Scan() {
		lineCount += 1
		text := sc.Text()
		rawText := text
		if commentIndex := strings.Index(text, "#"); commentIndex != -1 {
			text = text[:commentIndex]
		}
		text = strings.TrimSpace(text)
		if text == "" {
			// skip
			continue
		}
		if idx := strings.Index(text, ":"); idx > 0 {
			trimHead := strings.ToLower(text[:idx])
			trimTail := strings.ToLower(text[idx+1:])
			switch trimHead {
			case "full":
				domainFile.Domain = append(domainFile.Domain, trimTail)
			case "keyword":
				domainFile.KeyWord = append(domainFile.KeyWord, trimTail)
			case "regexp":
				domainFile.Regexp = append(domainFile.Regexp, trimTail)
			case "":
				return nil, &FileError{Reason: "empty matcher", Line: lineCount, Raw: rawText}
			default:
				return nil, &FileError{Reason: "bad matcher", Line: lineCount, Raw: rawText}
			}
		} else if idx == -1 {
			domainFile.Suffix = append(domainFile.Suffix, text)
		}
	}
	return domainFile, nil
}

type IPRuleset struct {
	Set *netipx.IPSet
}

func (rule *IPRuleset) Count() int64 {
	if rule == nil || rule.Set == nil {
		return 0
	}
	mips := (*myIPSet)(unsafe.Pointer(rule.Set))
	return int64(len(mips.rr))
}

func NewIPFile(path string) (*IPRuleset, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	ipFile := new(IPRuleset)
	sc := bufio.NewScanner(file)
	lineCount := -1
	var ipsetbuild netipx.IPSetBuilder
	for sc.Scan() {
		lineCount += 1
		text := sc.Text()
		rawText := text
		if commentIndex := strings.Index(text, "#"); commentIndex != -1 {
			text = text[:commentIndex]
		}
		text = strings.TrimSpace(text)
		if text == "" {
			// skip
			continue
		} else if prefix, err := netip.ParsePrefix(text); err == nil {
			ipsetbuild.AddPrefix(prefix)
			continue
		} else if ip, err := netip.ParseAddr(text); err == nil {
			ipsetbuild.Add(ip)
			continue
		}

		return nil, &FileError{Reason: "bad ip address or cidr", Line: lineCount, Raw: rawText}
	}
	ipFile.Set, err = ipsetbuild.IPSet()

	return ipFile, err
}

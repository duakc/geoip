package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strings"
	"unsafe"

	"github.com/duakc/geoip/trie"
	"github.com/duakc/geoip/trie/cidr"
	"github.com/goccy/go-yaml"
	"github.com/klauspost/compress/zstd"
	"github.com/sagernet/sing/common"
)

var MRSMagicBytes = [4]byte{'M', 'R', 'S', 1} // MRSv1

var (
	ErrDomainNotSupport          = errors.New("behavior `domain` only support `suffix` and `full` match, use `classical` instead")
	ErrClassicalNotSupportBinary = errors.New("behavior `classical` doesn't support MRS format")
)

const (
	MRSRuleBehaviorDomain    int = 0
	MRSRuleBehaviorIP        int = 1
	MRSRuleBehaviorClassical int = 2

	MRSFormatYAML   = "yaml"
	MRSFormatText   = "text"
	MRSFormatBinary = "mrs"
)

func MRSFormatToSuffix(format string, behavior int) string {
	if format == MRSFormatBinary && behavior == MRSRuleBehaviorClassical {
		panic("unsupported")
	}

	switch {
	case format == MRSFormatYAML:
		return ".yaml"
	case format == MRSFormatText:
		if behavior == MRSRuleBehaviorClassical {
			return ".classical"
		}
		return ".list"
	case format == MRSFormatBinary:
		return ".mrs"
	}
	panic("unexcepted format: " + format)
}

type MrsGenerator struct {
	Domain *DomainRuleset
	IP     *IPRuleset

	Behavior int
	Payload  []string
}

func (generator *MrsGenerator) Marshal(format string) ([]byte, error) {
	switch format {
	case MRSFormatYAML:
		return generator.MarshalYAML()
	case MRSFormatText:
		return generator.MarshalText()
	case MRSFormatBinary:
		return generator.MarshalBinary()
	}
	return nil, fmt.Errorf("unsupported format: %s", format)
}

func (generator *MrsGenerator) MarshalText() ([]byte, error) {
	if err := generator.fill(); err != nil {
		return nil, err
	}
	length := 0
	for i := 0; i < len(generator.Payload); i++ {
		length += len(generator.Payload[i]) + 1
	}
	buffer := bytes.NewBuffer(make([]byte, length))
	buffer.Reset()
	for i := 0; i < len(generator.Payload); i++ {
		const LF = '\n'
		buffer.WriteString(generator.Payload[i])
		buffer.WriteByte(LF)
	}
	return buffer.Bytes(), nil
}

func (generator *MrsGenerator) MarshalYAML() ([]byte, error) {
	if err := generator.fill(); err != nil {
		return nil, err
	}

	type schema struct {
		Payload []string `yaml:"payload"`
	}

	return yaml.Marshal(&schema{generator.Payload})
}

func (generator *MrsGenerator) MarshalBinary() (_ []byte, err error) {
	buffer := bytes.NewBuffer(nil)

	count := int64(0)
	switch generator.Behavior {
	case MRSRuleBehaviorIP:
		count = generator.IP.Count()
	case MRSRuleBehaviorDomain:
		count = int64(len(generator.Domain.Domain) + len(generator.Domain.Suffix))
	default:
		return nil, fmt.Errorf("unexcepted behavior: %d", generator.Behavior)
	}

	var zstdEncoder *zstd.Encoder
	zstdEncoder, err = zstd.NewWriter(buffer)
	if err != nil {
		return nil, err
	}

	var extra []byte
	err = writeGuard(zstdEncoder,
		writeBinaryOperation{Bytes: MRSMagicBytes[:]},
		writeBinaryOperation{Bytes: []byte{behaviorToByte(generator.Behavior)}},
		writeBinaryOperation{Binary: int64(count), Ending: binary.BigEndian},
		writeBinaryOperation{Binary: int64(len(extra)), Ending: binary.BigEndian, Bytes: extra},
	)
	if err != nil {
		return nil, err
	}
	switch generator.Behavior {
	case MRSRuleBehaviorDomain:
		domainTrie := trie.New[struct{}]()

		supportedDomain := &MrsGenerator{
			Domain: &DomainRuleset{
				Domain: generator.Domain.Domain,
				Suffix: generator.Domain.Suffix,
			},
			Behavior: generator.Behavior,
		}
		err = supportedDomain.fillDomainPayload()
		if err != nil {
			return nil, err
		}
		for i := 0; i < len(supportedDomain.Payload); i++ {
			err = domainTrie.Insert(supportedDomain.Payload[i], struct{}{})
			if err != nil {
				return nil, err
			}
		}

		set := domainTrie.NewDomainSet()
		err = set.WriteBin(zstdEncoder)
		if err != nil {
			return nil, err
		}
	case MRSRuleBehaviorIP:
		cidrIPSet := (*cidr.IpCidrSet)(unsafe.Pointer(generator.IP.Set))

		err := cidrIPSet.WriteBin(zstdEncoder)
		if err != nil {
			return nil, err
		}
	default:
		panic("unexcepted")
	}
	if err := zstdEncoder.Flush(); err != nil {
		return nil, err
	}
	if err := zstdEncoder.Close(); err != nil {
		return nil, err
	}

	return buffer.Bytes(), nil
}

func (generator *MrsGenerator) fill() error {
	switch generator.Behavior {
	case MRSRuleBehaviorDomain:
		return generator.fillDomainPayload()
	case MRSRuleBehaviorIP:
		generator.fillIPPayload()
	case MRSRuleBehaviorClassical:
		generator.fillClassical()
	default:
		return fmt.Errorf("unexcepted behavior: %d", generator.Behavior)
	}
	return nil
}

func (generator *MrsGenerator) fillDomainPayload() error {
	if len(generator.Domain.KeyWord) > 0 || len(generator.Domain.Regexp) > 0 {
		return ErrDomainNotSupport
	}
	generator.Payload = make([]string, 0, len(generator.Domain.Domain)+len(generator.Domain.Suffix))
	for i := 0; i < len(generator.Domain.Domain); i++ {
		generator.Payload = append(generator.Payload, generator.Domain.Domain[i])
	}
	for i := 0; i < len(generator.Domain.Suffix); i++ {
		suffix := generator.Domain.Suffix[i]
		if !strings.HasPrefix(suffix, ".") {
			suffix = "+." + suffix
		}
		generator.Payload = append(generator.Payload, suffix)
	}

	return nil
}

func (generator *MrsGenerator) fillClassical() {
	generator.Payload = make([]string, 0, generator.Domain.Count()+generator.IP.Count())

	if generator.IP != nil && generator.IP.Set != nil {
		for _, prefix := range common.Map(generator.IP.Set.Prefixes(), netip.Prefix.String) {
			generator.Payload = append(generator.Payload, "IP-CIDR,"+prefix)
		}
	}
	for i := 0; generator.Domain != nil && i < len(generator.Domain.Domain); i++ {
		generator.Payload = append(generator.Payload, "DOMAIN,"+generator.Domain.Domain[i])
	}
	for i := 0; generator.Domain != nil && i < len(generator.Domain.Suffix); i++ {
		generator.Payload = append(generator.Payload, "DOMAIN-SUFFIX,"+generator.Domain.Suffix[i])
	}
	for i := 0; generator.Domain != nil && i < len(generator.Domain.KeyWord); i++ {
		generator.Payload = append(generator.Payload, "DOMAIN-KEYWORD,"+generator.Domain.KeyWord[i])
	}
	for i := 0; generator.Domain != nil && i < len(generator.Domain.Regexp); i++ {
		generator.Payload = append(generator.Payload, "DOMAIN-REGEX,"+generator.Domain.Regexp[i])
	}
}
func (generator *MrsGenerator) fillIPPayload() {
	count := generator.IP.Count()
	if count == 0 {
		generator.Payload = []string{}
		return
	}
	generator.Payload = make([]string, count)
	copy(generator.Payload[:count], common.Map(generator.IP.Set.Prefixes(), netip.Prefix.String))
}

func (rule *DomainRuleset) WriteMRS(w io.Writer, format string, behavior int) error {
	switch {
	case rule.Count() == 0:
		return ErrEmpty
	case behavior != MRSRuleBehaviorDomain && behavior != MRSRuleBehaviorClassical:
		return fmt.Errorf("unexcepted behavior: %d", behavior)
	case (len(rule.KeyWord) != 0 || len(rule.Regexp) != 0) && behavior != MRSRuleBehaviorClassical:
		return ErrDomainNotSupport
	}
	generator := &MrsGenerator{
		Domain:   rule,
		Behavior: behavior,
	}
	gen, err := generator.Marshal(format)
	if err != nil {
		return err
	}
	_, err = w.Write(gen)
	return err
}

func (rule *IPRuleset) WriteMRS(w io.Writer, format string, behavior int) error {
	switch {
	case rule.Count() == 0:
		return ErrEmpty
	case behavior != MRSRuleBehaviorIP && behavior != MRSRuleBehaviorClassical:
		return fmt.Errorf("unexcepted behavior: %d", behavior)
	case behavior == MRSRuleBehaviorClassical && format == MRSFormatBinary:
		return ErrClassicalNotSupportBinary
	}
	generator := &MrsGenerator{
		IP:       rule,
		Behavior: behavior,
	}
	gen, err := generator.Marshal(format)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	_, err = w.Write(gen)
	return err
}

func behaviorToByte(b int) byte {
	switch b {
	case 0:
		return byte(0)
	case 1:
		return byte(1)
	default:
		panic("unknown behavior")
	}
}

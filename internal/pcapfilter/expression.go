// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pcapfilter

import (
	"errors"
	"fmt"
	"strings"

	cbpf "golang.org/x/net/bpf"
)

type exprOp uint8

const (
	exprLeaf exprOp = iota
	exprAnd
	exprOr
	exprNot
)

type exprNode struct {
	op          exprOp
	text        string
	left, right *exprNode
}

type exprToken struct {
	text string
	pos  int
}

// parseFilterExpression parses the boolean structure of a tcpdump-style
// expression. Operators follow pcap-filter(7): negation binds tightest, and
// and/or have equal precedence and associate left to right.
func parseFilterExpression(src string) (*exprNode, error) {
	if strings.TrimSpace(src) == "" {
		return nil, ErrEmptyFilter
	}

	parser := exprParser{
		src:    src,
		tokens: mergeDirectionQualifiers(lexFilterExpression(src)),
	}

	node, err := parser.parseExpr()
	if err != nil {
		return nil, err
	}
	if parser.pos != len(parser.tokens) {
		token := parser.tokens[parser.pos]
		return nil, parser.errorf(token.pos, "unexpected token %q", token.text)
	}

	return node, nil
}

type exprParser struct {
	src           string
	tokens        []exprToken
	pos           int
	lastQualifier string
}

func (p *exprParser) parseExpr() (*exprNode, error) {
	left, err := p.parseOperand()
	if err != nil {
		return nil, err
	}

	for p.pos < len(p.tokens) {
		token := p.tokens[p.pos]
		if token.text != "and" && token.text != "or" {
			break
		}

		p.pos++
		right, err := p.parseOperand()
		if err != nil {
			return nil, err
		}

		op := exprAnd
		if token.text == "or" {
			op = exprOr
		}
		left = &exprNode{op: op, left: left, right: right}
	}

	return left, nil
}

func (p *exprParser) parseOperand() (*exprNode, error) {
	if p.pos >= len(p.tokens) {
		return nil, p.errorf(len(p.src), "unexpected end of expression")
	}

	token := p.tokens[p.pos]
	switch token.text {
	case "not":
		p.pos++
		child, err := p.parseOperand()
		if err != nil {
			return nil, err
		}
		return &exprNode{op: exprNot, left: child}, nil
	case "(":
		p.pos++
		// A complete qualifier list (one with a type) stays inheritable
		// inside the group: tcpdump reads "src host A or (B or C)" as
		// "src host A or (src host B or src host C)".
		if !hasQualifierType(p.lastQualifier) {
			p.lastQualifier = ""
		}
		node, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if p.pos >= len(p.tokens) || p.tokens[p.pos].text != ")" {
			return nil, p.errorf(token.pos, "unclosed parenthesis")
		}
		p.pos++
		// tcpdump's "most recent keyword" rule is lexical, so a following
		// primitive inherits from the group's rightmost primitive.
		p.lastQualifier = ""
		if leaf := lastLeaf(node); leaf != nil {
			p.lastQualifier = filterQualifierPrefix(lexFilterExpression(leaf.text))
		}
		return node, nil
	case ")", "and", "or":
		return nil, p.errorf(token.pos, "unexpected token %q", token.text)
	}

	start := p.pos
scan:
	for p.pos < len(p.tokens) {
		switch p.tokens[p.pos].text {
		case "and", "or", "(", ")":
			break scan
		}
		p.pos++
	}
	if p.pos == start {
		return nil, p.errorf(token.pos, "expected filter primitive")
	}

	return &exprNode{op: exprLeaf, text: p.expandLeaf(p.tokens[start:p.pos])}, nil
}

// expandLeaf reapplies tcpdump's omitted-qualifier shorthand. go-pcap copies
// the previous primitive's qualifiers onto a primitive with none of its own,
// so a bare operand after "tcp dst port 80 or" means "tcp dst port 443".
func (p *exprParser) expandLeaf(tokens []exprToken) string {
	text := joinExprTokens(tokens)
	if len(tokens) == 1 && !isFilterKeyword(tokens[0].text) && p.lastQualifier != "" {
		return p.lastQualifier + " " + text
	}
	if prefix := filterQualifierPrefix(tokens); prefix != "" {
		p.lastQualifier = prefix
	} else {
		p.lastQualifier = ""
	}
	return text
}

func filterQualifierPrefix(tokens []exprToken) string {
	lastType := -1
	for i, token := range tokens {
		switch token.text {
		case "host", "net", "port", "portrange", "proto":
			lastType = i
		}
	}
	if lastType >= 0 {
		if lastType+1 >= len(tokens) {
			return ""
		}
		prefix := make([]exprToken, 0, lastType+1)
		for _, token := range tokens[:lastType+1] {
			if token.text != "not" {
				prefix = append(prefix, token)
			}
		}
		return joinExprTokens(prefix)
	}
	if len(tokens) == 1 && isProtocolKeyword(tokens[0].text) {
		return tokens[0].text
	}
	return ""
}

var filterKeywords = map[string]bool{
	"ether": true, "fddi": true, "tr": true, "wlan": true,
	"ip": true, "ip4": true, "ip6": true, "arp": true, "rarp": true,
	"decnet": true, "tcp": true, "udp": true, "icmp": true, "icmp6": true,
	"igmp": true, "igrp": true, "pim": true, "ah": true, "esp": true,
	"vrrp": true, "stp": true, "sctp": true,
	"host": true, "net": true, "port": true, "portrange": true, "proto": true,
	"src": true, "dst": true, "src and dst": true, "src or dst": true,
	"gateway": true, "multicast": true, "broadcast": true, "less": true,
	"greater": true, "vlan": true, "mpls": true, "pppoes": true,
	"inbound": true, "outbound": true, "ifindex": true, "llc": true,
	"and": true, "or": true, "not": true,
}

var filterProtocolKeywords = map[string]bool{
	"ether": true, "fddi": true, "tr": true, "wlan": true,
	"ip": true, "ip4": true, "ip6": true, "arp": true, "rarp": true,
	"decnet": true, "tcp": true, "udp": true, "icmp": true, "icmp6": true,
	"igmp": true, "igrp": true, "pim": true, "ah": true, "esp": true,
	"vrrp": true, "stp": true,
}

func hasQualifierType(prefix string) bool {
	for _, token := range lexFilterExpression(prefix) {
		switch token.text {
		case "host", "net", "port", "portrange", "proto":
			return true
		}
	}
	return false
}

func isFilterKeyword(word string) bool {
	return filterKeywords[word]
}

func isProtocolKeyword(word string) bool {
	return filterProtocolKeywords[word]
}

func lastLeaf(node *exprNode) *exprNode {
	if node == nil {
		return nil
	}
	switch node.op {
	case exprLeaf:
		return node
	case exprNot:
		return lastLeaf(node.left)
	default:
		return lastLeaf(node.right)
	}
}

func (p *exprParser) errorf(pos int, format string, args ...any) error {
	return fmt.Errorf(
		"%w: %q: %s at byte %d",
		ErrInvalidFilter,
		p.src,
		fmt.Sprintf(format, args...),
		pos,
	)
}

func lexFilterExpression(src string) []exprToken {
	tokens := make([]exprToken, 0, 8)

	for i := 0; i < len(src); {
		switch {
		case isExprSpace(src[i]):
			i++
		case src[i] == '(' || src[i] == ')':
			tokens = append(tokens, exprToken{text: string(src[i]), pos: i})
			i++
		default:
			start := i
			for i < len(src) && !isExprSpace(src[i]) && src[i] != '(' && src[i] != ')' {
				i++
			}
			tokens = append(tokens, exprToken{text: src[start:i], pos: start})
		}
	}

	return tokens
}

func isExprSpace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r'
}

// mergeDirectionQualifiers keeps "src and dst" and "src or dst" inside the
// leaf they qualify instead of treating the middle word as a boolean operator.
func mergeDirectionQualifiers(tokens []exprToken) []exprToken {
	merged := make([]exprToken, 0, len(tokens))

	for i := 0; i < len(tokens); i++ {
		if i+2 < len(tokens) &&
			tokens[i].text == "src" &&
			(tokens[i+1].text == "and" || tokens[i+1].text == "or") &&
			tokens[i+2].text == "dst" {
			merged = append(merged, exprToken{
				text: "src " + tokens[i+1].text + " dst",
				pos:  tokens[i].pos,
			})
			i += 2
			continue
		}
		merged = append(merged, tokens[i])
	}

	return merged
}

func joinExprTokens(tokens []exprToken) string {
	var builder strings.Builder
	for i, token := range tokens {
		if i > 0 {
			builder.WriteByte(' ')
		}
		builder.WriteString(token.text)
	}
	return builder.String()
}

type filterProgram struct {
	insts   []cbpf.Instruction
	keepIdx int
	dropIdx int
}

func compileFilterExpression(expr string) ([]cbpf.Instruction, error) {
	node, err := parseFilterExpression(expr)
	if err != nil {
		return nil, err
	}

	program, err := compileNode(node)
	if err != nil {
		return nil, err
	}

	return program.insts, nil
}

func compileNode(node *exprNode) (filterProgram, error) {
	switch node.op {
	case exprLeaf:
		return compileLeaf(node.text)
	case exprNot:
		child, err := compileNode(node.left)
		if err != nil {
			return filterProgram{}, err
		}
		return invertProgram(child)
	case exprAnd, exprOr:
		left, err := compileNode(node.left)
		if err != nil {
			return filterProgram{}, err
		}
		right, err := compileNode(node.right)
		if err != nil {
			return filterProgram{}, err
		}
		return composePrograms(left, right, node.op == exprAnd)
	default:
		return filterProgram{}, fmt.Errorf(
			"%w: unknown expression node %d",
			ErrInvalidFilter,
			node.op,
		)
	}
}

// compileLeaf compiles one primitive with go-pcap. Composites are composed by
// this file instead: go-pcap drops terms and miswires branches when an
// expression contains and/or.
func compileLeaf(text string) (filterProgram, error) {
	insns, err := compileGoPcap(text)
	if err != nil {
		if errors.Is(err, ErrEmptyFilter) {
			return filterProgram{}, err
		}
		return filterProgram{}, fmt.Errorf(
			"%w: leaf %q: %w",
			ErrInvalidFilter,
			text,
			err,
		)
	}

	if len(insns) < 2 {
		return filterProgram{}, fmt.Errorf(
			"%w: leaf %q compiled to %d instructions",
			ErrInvalidFilter,
			text,
			len(insns),
		)
	}

	secondLast, secondLastOK := insns[len(insns)-2].(cbpf.RetConstant)
	last, lastOK := insns[len(insns)-1].(cbpf.RetConstant)
	if !secondLastOK || !lastOK {
		return filterProgram{}, fmt.Errorf(
			"%w: leaf %q has an unsupported return layout",
			ErrInvalidFilter,
			text,
		)
	}

	var keepIdx, dropIdx int
	switch {
	case secondLast.Val != 0 && last.Val == 0:
		keepIdx, dropIdx = len(insns)-2, len(insns)-1
	case secondLast.Val == 0 && last.Val != 0:
		keepIdx, dropIdx = len(insns)-1, len(insns)-2
	default:
		return filterProgram{}, fmt.Errorf(
			"%w: leaf %q has an unsupported return layout",
			ErrInvalidFilter,
			text,
		)
	}

	for i := range insns[:len(insns)-2] {
		if _, ok := insns[i].(cbpf.RetConstant); ok {
			return filterProgram{}, fmt.Errorf(
				"%w: leaf %q compiled to a composite expression",
				ErrInvalidFilter,
				text,
			)
		}
	}

	insns = boundJumpsTo(insns, dropIdx)
	return filterProgram{
		insts:   insns,
		keepIdx: keepIdx,
		dropIdx: dropIdx,
	}, nil
}

func (p filterProgram) validate() error {
	if p.keepIdx < 0 || p.dropIdx < 0 ||
		p.keepIdx >= len(p.insts) || p.dropIdx >= len(p.insts) ||
		p.keepIdx == p.dropIdx {
		return fmt.Errorf("%w: invalid filter program terminals", ErrInvalidFilter)
	}

	keep, ok := p.insts[p.keepIdx].(cbpf.RetConstant)
	if !ok || keep.Val == 0 {
		return fmt.Errorf(
			"%w: filter program keep terminal is not an accept return",
			ErrInvalidFilter,
		)
	}
	drop, ok := p.insts[p.dropIdx].(cbpf.RetConstant)
	if !ok || drop.Val != 0 {
		return fmt.Errorf(
			"%w: filter program drop terminal is not a reject return",
			ErrInvalidFilter,
		)
	}

	return nil
}

func composePrograms(left, right filterProgram, isAnd bool) (filterProgram, error) {
	if err := left.validate(); err != nil {
		return filterProgram{}, err
	}
	if err := right.validate(); err != nil {
		return filterProgram{}, err
	}

	out := make([]cbpf.Instruction, 0, len(left.insts)+len(right.insts))
	out = append(out, left.insts...)
	out = append(out, right.insts...)

	bStart := len(left.insts)
	bKeep := bStart + right.keepIdx
	bDrop := bStart + right.dropIdx

	keepTarget, dropTarget := bStart, bDrop
	if !isAnd {
		keepTarget, dropTarget = bKeep, bStart
	}

	keepSkip, err := forwardSkip(left.keepIdx, keepTarget)
	if err != nil {
		return filterProgram{}, err
	}
	dropSkip, err := forwardSkip(left.dropIdx, dropTarget)
	if err != nil {
		return filterProgram{}, err
	}

	out[left.keepIdx] = cbpf.Jump{Skip: keepSkip}
	out[left.dropIdx] = cbpf.Jump{Skip: dropSkip}

	return filterProgram{
		insts:   out,
		keepIdx: bKeep,
		dropIdx: bDrop,
	}, nil
}

func forwardSkip(from, target int) (uint32, error) {
	if target <= from {
		return 0, fmt.Errorf(
			"%w: non-forward filter jump from instruction %d to %d",
			ErrInvalidFilter,
			from,
			target,
		)
	}
	return uint32(target - from - 1), nil
}

func invertProgram(program filterProgram) (filterProgram, error) {
	if err := program.validate(); err != nil {
		return filterProgram{}, err
	}

	keep := program.insts[program.keepIdx].(cbpf.RetConstant)
	drop := program.insts[program.dropIdx].(cbpf.RetConstant)

	program.insts[program.keepIdx] = drop
	program.insts[program.dropIdx] = keep
	program.keepIdx, program.dropIdx = program.dropIdx, program.keepIdx

	return program, nil
}

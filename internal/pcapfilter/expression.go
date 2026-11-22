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
	"fmt"
	"strings"
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
	src    string
	tokens []exprToken
	pos    int
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
		node, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if p.pos >= len(p.tokens) || p.tokens[p.pos].text != ")" {
			return nil, p.errorf(token.pos, "unclosed parenthesis")
		}
		p.pos++
		return node, nil
	case ")", "and", "or":
		return nil, p.errorf(token.pos, "unexpected token %q", token.text)
	}

	start := p.pos
scan:
	for p.pos < len(p.tokens) {
		switch p.tokens[p.pos].text {
		case "and", "or", "not", "(", ")":
			break scan
		}
		p.pos++
	}
	if p.pos == start {
		return nil, p.errorf(token.pos, "expected filter primitive")
	}

	return &exprNode{op: exprLeaf, text: joinExprTokens(p.tokens[start:p.pos])}, nil
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

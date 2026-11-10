// Copyright 2026 Christopher O'Connell
// SPDX-License-Identifier: Apache-2.0

package formal

import "github.com/jwriteclub/spec/internal/pos"

// TokenKind says what class of text a Token holds, for highlighting. It is
// language-neutral; a language's lexer decides what falls in each class.
type TokenKind int

// Token kinds.
const (
	TokenKeyword TokenKind = iota + 1
	TokenOperator
	TokenString
	TokenNumber
	TokenComment
	// TokenDefinition is a name the lexer knows is being declared: the module's.
	// Other declarations come from File.Decls.
	TokenDefinition
)

// Token is a run of source bytes of one class. Identifiers and white space
// are not tokens: they are drawn plainly.
type Token struct {
	Kind  TokenKind
	Range pos.Range
}

// Lexer splits a model's source into tokens, in order and without overlap. It
// must not fail, and must tolerate any bytes: an editor buffer is often
// mid-edit.
type Lexer func(src []byte) []Token

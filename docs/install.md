<!-- Copyright 2026 Christopher O'Connell -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Install

`spec` is a single Go binary. Building it needs [Go 1.26 or later](https://go.dev/doc/install).

Install it with Go:

    go install github.com/jwriteclub/spec/cmd/spec@latest

You might want to add this line to your path (on Mac or Linux)

    export PATH="$PATH:$(go env GOPATH)/bin"

To build from a checkout instead:

    git clone https://github.com/jwriteclub/spec
    cd spec
    go build -o spec ./cmd/spec

`spec doctor` checks the project, git, your user config and the model provider, and says how to fix
what is wrong.

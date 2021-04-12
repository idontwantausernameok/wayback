// Copyright 2021 Wayback Archiver. All rights reserved.
// Use of this source code is governed by the GNU GPL v3
// license that can be found in the LICENSE file.

package publish // import "github.com/wabarc/wayback/publish"

import (
	"bytes"
	"context"
	"net/http"
	"text/template"
	"time"

	"github.com/google/go-github/v33/github"
	"github.com/wabarc/wayback"
	"github.com/wabarc/wayback/config"
	"github.com/wabarc/wayback/logger"
)

type GitHub struct {
	client *http.Client
}

func NewGitHub() *GitHub {
	return &GitHub{}
}

func ToIssues(ctx context.Context, text string) bool {
	if config.Opts.GitHubToken() == "" {
		logger.Error("[publish] GitHub personal access token is required")
		return false
	}

	// Authenticated user must grant repo:public_repo scope,
	// private repository need whole repo scope.
	auth := github.BasicAuthTransport{
		Username: config.Opts.GitHubOwner(),
		Password: config.Opts.GitHubToken(),
	}
	client := github.NewClient(auth.Client())

	if config.Opts.HasDebugMode() {
		user, _, _ := client.Users.Get(ctx, "")
		logger.Debug("[publish] authorized GitHub user: %v", user)
	}

	// Create an issue to GitHub
	t := "Published at " + time.Now().Format("2006-01-02T15:04:05")
	ir := &github.IssueRequest{Title: github.String(t), Body: github.String(text)}
	issue, _, err := client.Issues.Create(ctx, config.Opts.GitHubOwner(), config.Opts.GitHubRepo(), ir)
	if err != nil {
		logger.Debug("[publish] create issue failed: %v", err)
		return false
	}
	logger.Debug("[publish] created issue: %v", issue)

	return true
}

func (gh *GitHub) Render(vars []*wayback.Collect) string {
	var tmplBytes bytes.Buffer

	const tmpl = `{{range $ := .}}**[{{ $.Arc }}]({{ $.Ext }})**:
{{ range $src, $dst := $.Dst -}}
> origin: {{ $src }}
> archived: {{ $dst }}

{{end}}
{{end}}`

	tpl, err := template.New("message").Parse(tmpl)
	if err != nil {
		logger.Debug("[publish] parse template failed, %v", err)
		return ""
	}

	err = tpl.Execute(&tmplBytes, vars)
	if err != nil {
		logger.Debug("[publish] execute template failed, %v", err)
		return ""
	}

	return tmplBytes.String()
}

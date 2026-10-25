// Package graphql is the http -> postgresql flow: the gateway asks the graphql
// service for books over GraphQL (JSON over HTTP/1.1), the service keeps
// authors and books in postgresql.
package graphql

import (
	_ "embed"
	"strings"

	"github.com/kubeshark/demo-apps/internal/markers"
)

const Name = "graphql"

//go:embed markers.tsv
var markersTSV string

var Markers = markers.MustParse(Name, markersTSV)

const UserHeader = "X-Demo-User"

type Book struct {
	ID     int    `json:"id"`
	Title  string `json:"title"`
	Author string `json:"author"`
}

// Books pairs AUTHOR and TITLE markers by their trailing segment.
func Books() []Book {
	titles := map[string]string{}
	for _, t := range Markers.Field("TITLE") {
		titles[suffix(t)] = t
	}
	var out []Book
	for _, a := range Markers.Field("AUTHOR") {
		title, ok := titles[suffix(a)]
		if !ok {
			panic("graphql markers: AUTHOR without matching TITLE: " + a)
		}
		out = append(out, Book{Title: title, Author: a})
	}
	return out
}

func suffix(v string) string {
	return v[strings.LastIndex(v, "-")+1:]
}

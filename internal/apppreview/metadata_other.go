//go:build !unix

package apppreview

import "errors"

type MetadataServer struct{}

func StartMetadata(string, *Registry) (*MetadataServer, error) {
	return nil, errors.New("project preview launch metadata requires a Unix host")
}

func (*MetadataServer) Close() error { return nil }

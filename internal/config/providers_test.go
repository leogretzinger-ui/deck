package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestProviderEntriesCoverEveryConfiguredProvider(t *testing.T) {
	var providers Providers
	entries := providers.Entries()
	value := reflect.ValueOf(&providers).Elem()
	if len(entries) != value.NumField() {
		t.Fatalf("discovery covers %d providers, config has %d", len(entries), value.NumField())
	}
	for i := range value.NumField() {
		field := value.Type().Field(i)
		wantName := strings.ReplaceAll(field.Tag.Get("yaml"), "_", "-")
		found := false
		for _, entry := range entries {
			if entry.Name == wantName && entry.Config == value.Field(i).Addr().Interface().(*Provider) && entry.Binary != "" {
				found = true
			}
		}
		if !found {
			t.Errorf("provider %s has no executable discovery entry", wantName)
		}
	}
}

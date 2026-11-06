package main

import (
	"reflect"
	"testing"

	"devsandbox/internal/sockrelay"
)

func TestParseRelayArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    []sockrelay.GuestSocket
		wantErr bool
	}{
		{
			name: "name with primary path and alias",
			args: []string{"herdr.sock=/home/sandboxuser/.run/7/herdr.sock,/home/sandboxuser/.config/herdr/herdr.sock"},
			want: []sockrelay.GuestSocket{{
				Name:  "herdr.sock",
				Paths: []string{"/home/sandboxuser/.run/7/herdr.sock", "/home/sandboxuser/.config/herdr/herdr.sock"},
			}},
		},
		{
			name: "two sockets",
			args: []string{"herdr.sock=/r/herdr.sock", "kitty.sock=/r/kitty.sock"},
			want: []sockrelay.GuestSocket{
				{Name: "herdr.sock", Paths: []string{"/r/herdr.sock"}},
				{Name: "kitty.sock", Paths: []string{"/r/kitty.sock"}},
			},
		},
		{name: "no sockets", args: nil, wantErr: true},
		{name: "missing separator", args: []string{"herdr.sock"}, wantErr: true},
		{name: "empty name", args: []string{"=/r/a.sock"}, wantErr: true},
		{name: "relative path", args: []string{"a.sock=r/a.sock"}, wantErr: true},
		{name: "empty path in list", args: []string{"a.sock=/r/a.sock,"}, wantErr: true},
		{name: "duplicate name", args: []string{"a.sock=/r/a.sock", "a.sock=/r/b.sock"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRelayArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

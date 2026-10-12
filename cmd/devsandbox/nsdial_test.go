package main

import (
	"bytes"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"devsandbox/internal/portforward"
)

type nsDialOutput struct {
	bytes.Buffer
	writeErr error
	closed   bool
}

func (o *nsDialOutput) Write(p []byte) (int, error) {
	if o.writeErr != nil {
		return 0, o.writeErr
	}
	return o.Buffer.Write(p)
}

func (o *nsDialOutput) Close() error {
	o.closed = true
	return nil
}

func nsDialListener(t *testing.T) *net.TCPListener {
	t.Helper()
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := listener.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return listener
}

func TestRunNSDial_ReadinessAndHalfClose(t *testing.T) {
	listener := nsDialListener(t)
	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.AcceptTCP()
		if err != nil {
			serverErr <- err
			return
		}
		defer func() { _ = conn.Close() }()
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			serverErr <- err
			return
		}
		request, err := io.ReadAll(conn)
		if err != nil {
			serverErr <- err
			return
		}
		// Reply only after stdin EOF has become a TCP write-half close.
		_, err = conn.Write(append([]byte("reply:"), request...))
		serverErr <- err
	}()

	var output nsDialOutput
	if err := runNSDialIO(listener.Addr().String(), strings.NewReader("request"), &output); err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	want := append([]byte{portforward.NamespaceDialReady}, []byte("reply:request")...)
	if !bytes.Equal(output.Bytes(), want) {
		t.Fatalf("output = %q, want %q", output.Bytes(), want)
	}
	if !output.closed {
		t.Fatal("stdout not closed after TCP EOF")
	}
}

func TestRunNSDial_DialFailureDoesNotSignalReady(t *testing.T) {
	var output nsDialOutput
	err := runNSDialIO("invalid-address", strings.NewReader(""), &output)
	if err == nil || !strings.Contains(err.Error(), "dial invalid-address:") {
		t.Fatalf("error = %v, want dial error", err)
	}
	if output.Len() != 0 {
		t.Fatalf("failed dial produced stdout %q", output.Bytes())
	}
}

func TestRunNSDial_ReadinessWriteFailure(t *testing.T) {
	listener := nsDialListener(t)
	writeErr := errors.New("stdout unavailable")
	output := nsDialOutput{writeErr: writeErr}
	err := runNSDialIO(listener.Addr().String(), strings.NewReader(""), &output)
	if !errors.Is(err, writeErr) || !strings.Contains(err.Error(), "write readiness:") {
		t.Fatalf("error = %v, want readiness write error", err)
	}
	conn, err := listener.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err := conn.Read(b[:]); !errors.Is(err, io.EOF) {
		t.Fatalf("connection not closed after failed readiness: %v", err)
	}
}

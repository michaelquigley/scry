package notify

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type smtpDelivery struct {
	message  []byte
	startTLS bool
	err      error
}

func listenSMTP(t *testing.T) (net.Listener, string, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, rawPort, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	return listener, host, port
}

func writeSMTP(writer *bufio.Writer, response string) error {
	if _, err := writer.WriteString(response); err != nil {
		return err
	}
	return writer.Flush()
}

func readSMTP(reader *bufio.Reader, prefix string) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if !strings.HasPrefix(line, prefix) {
		return "", fmt.Errorf("smtp command %q does not start with %q", line, prefix)
	}
	return line, nil
}

func receiveSMTP(connection net.Conn, certificate *tls.Certificate, recipients int) smtpDelivery {
	defer connection.Close()
	reader := bufio.NewReader(connection)
	writer := bufio.NewWriter(connection)
	if err := writeSMTP(writer, "220 smtp.test ESMTP\r\n"); err != nil {
		return smtpDelivery{err: err}
	}
	if _, err := readSMTP(reader, "EHLO "); err != nil {
		return smtpDelivery{err: err}
	}

	startedTLS := false
	if certificate != nil {
		if err := writeSMTP(writer, "250-smtp.test\r\n250 STARTTLS\r\n"); err != nil {
			return smtpDelivery{err: err}
		}
		if _, err := readSMTP(reader, "STARTTLS"); err != nil {
			return smtpDelivery{err: err}
		}
		if err := writeSMTP(writer, "220 ready for tls\r\n"); err != nil {
			return smtpDelivery{err: err}
		}
		tlsConnection := tls.Server(connection, &tls.Config{
			Certificates: []tls.Certificate{*certificate},
			MinVersion:   tls.VersionTLS12,
		})
		if err := tlsConnection.Handshake(); err != nil {
			return smtpDelivery{err: err}
		}
		connection = tlsConnection
		reader = bufio.NewReader(connection)
		writer = bufio.NewWriter(connection)
		if _, err := readSMTP(reader, "EHLO "); err != nil {
			return smtpDelivery{err: err}
		}
		startedTLS = true
	}
	if err := writeSMTP(writer, "250 smtp.test\r\n"); err != nil {
		return smtpDelivery{err: err}
	}
	if _, err := readSMTP(reader, "MAIL FROM:<scry@example.com>"); err != nil {
		return smtpDelivery{err: err}
	}
	if err := writeSMTP(writer, "250 sender ok\r\n"); err != nil {
		return smtpDelivery{err: err}
	}
	for range recipients {
		if _, err := readSMTP(reader, "RCPT TO:<"); err != nil {
			return smtpDelivery{err: err}
		}
		if err := writeSMTP(writer, "250 recipient ok\r\n"); err != nil {
			return smtpDelivery{err: err}
		}
	}
	if _, err := readSMTP(reader, "DATA"); err != nil {
		return smtpDelivery{err: err}
	}
	if err := writeSMTP(writer, "354 send message\r\n"); err != nil {
		return smtpDelivery{err: err}
	}
	message, err := io.ReadAll(textproto.NewReader(reader).DotReader())
	if err != nil {
		return smtpDelivery{err: err}
	}
	if err := writeSMTP(writer, "250 queued\r\n"); err != nil {
		return smtpDelivery{err: err}
	}
	if _, err := readSMTP(reader, "QUIT"); err != nil {
		return smtpDelivery{err: err}
	}
	if err := writeSMTP(writer, "221 bye\r\n"); err != nil {
		return smtpDelivery{err: err}
	}
	return smtpDelivery{message: message, startTLS: startedTLS}
}

func TestSMTPUsesSTARTTLSAndSendsSharedMessage(t *testing.T) {
	certificateServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	certificate := certificateServer.TLS.Certificates[0]
	roots := x509.NewCertPool()
	roots.AddCert(certificateServer.Certificate())
	certificateServer.Close()

	listener, host, port := listenSMTP(t)
	defer listener.Close()
	delivery := make(chan smtpDelivery, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			delivery <- smtpDelivery{err: err}
			return
		}
		delivery <- receiveSMTP(connection, &certificate, 2)
	}()

	notifier, err := NewSMTP(
		host,
		port,
		"scry@example.com",
		[]string{"one@example.com", "Two <two@example.com>"},
	)
	if err != nil {
		t.Fatal(err)
	}
	notifier.tlsConfig.RootCAs = roots
	transition := testTransition("nas-snapshot")
	if err := notifier.Notify(context.Background(), transition); err != nil {
		t.Fatal(err)
	}
	received := <-delivery
	if received.err != nil {
		t.Fatal(received.err)
	}
	if !received.startTLS {
		t.Fatal("smtp delivery did not use STARTTLS")
	}

	message, err := mail.ReadMessage(bytes.NewReader(received.message))
	if err != nil {
		t.Fatal(err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(message.Header.Get("Subject"))
	if err != nil {
		t.Fatal(err)
	}
	if subject != Subject(transition) {
		t.Fatalf("subject: %q", subject)
	}
	body, err := io.ReadAll(message.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "detail: snapshot exited 2") {
		t.Fatalf("body:\n%s", body)
	}
}

func TestSMTPHonorsContextCancellationDuringStalledGreeting(t *testing.T) {
	listener, host, port := listenSMTP(t)
	defer listener.Close()
	accepted := make(chan struct{})
	peerClosed := make(chan struct{})
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			close(peerClosed)
			return
		}
		defer connection.Close()
		close(accepted)
		var data [1]byte
		_, _ = connection.Read(data[:])
		close(peerClosed)
	}()

	notifier, err := NewSMTP(host, port, "scry@example.com", []string{"one@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- notifier.Notify(ctx, testTransition("job"))
	}()
	<-accepted
	cancel()
	if err := <-result; err == nil {
		t.Fatal("stalled smtp greeting survived cancellation")
	}
	<-peerClosed
}

func TestSMTPStalledAttemptsExpireAndQueueAdvances(t *testing.T) {
	listener, host, port := listenSMTP(t)
	defer listener.Close()
	var attempts atomic.Int32
	delivered := make(chan smtpDelivery, 1)
	go func() {
		for attempt := 1; attempt <= 6; attempt++ {
			connection, err := listener.Accept()
			if err != nil {
				delivered <- smtpDelivery{err: err}
				return
			}
			attempts.Add(1)
			if attempt <= 5 {
				var data [1]byte
				_, _ = connection.Read(data[:])
				_ = connection.Close()
				continue
			}
			delivered <- receiveSMTP(connection, nil, 1)
		}
	}()

	notifier, err := NewSMTP(host, port, "scry@example.com", []string{"one@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := NewDispatcher([]Destination{{Name: "smtp", Notifier: notifier}})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher.attemptTimeout = 20 * time.Millisecond
	dispatcher.backoffs = []time.Duration{0, 0, 0, 0}
	dispatcher.wait = func(context.Context, time.Duration) bool { return true }
	dispatcher.Enqueue(testTransition("stalled"))
	dispatcher.Enqueue(testTransition("next"))
	cancel, runErr := startDispatcher(t, dispatcher)

	received := <-delivered
	if received.err != nil {
		t.Fatal(received.err)
	}
	if got := attempts.Load(); got != 6 {
		t.Fatalf("attempts: %d, want 6", got)
	}
	stopDispatcher(t, cancel, runErr)
}

func TestNewSMTPRejectsInvalidAddresses(t *testing.T) {
	tests := []struct {
		name string
		host string
		port int
		from string
		to   []string
	}{
		{name: "missing host", port: 25, from: "scry@example.com", to: []string{"one@example.com"}},
		{name: "bad port", host: "smtp", from: "scry@example.com", to: []string{"one@example.com"}},
		{name: "bad from", host: "smtp", port: 25, from: "bad", to: []string{"one@example.com"}},
		{name: "missing recipients", host: "smtp", port: 25, from: "scry@example.com"},
		{name: "bad recipient", host: "smtp", port: 25, from: "scry@example.com", to: []string{"bad"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewSMTP(test.host, test.port, test.from, test.to); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

package udp_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/dmksnnk/star/internal/platform"
	"github.com/dmksnnk/star/internal/platform/udp"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestUDPConn(t *testing.T) {
	t.Run("simple", func(t *testing.T) {
		stcConn, customConn := testPipe(t)

		for i := range 10 {
			msg := []byte("hello " + strconv.Itoa(i))
			_, err := stcConn.Write(msg)
			if err != nil {
				t.Fatalf("failed to write to c1: %v", err)
			}
			buf := make([]byte, len(msg))
			n, err := customConn.Read(buf)
			if err != nil {
				t.Fatalf("failed to read from c2: %v", err)
			}

			if !bytes.Equal(buf[:n], msg) {
				t.Fatalf("expected: %s, got: %s", msg, buf[:n])
			}
		}
	})

	t.Run("ping pong", func(t *testing.T) {
		stcConn, customConn := testPipe(t)
		var wg sync.WaitGroup
		var start uint64 = 0

		pingPonger := func(c net.Conn) {
			buf := make([]byte, 8)
			var prev uint64
			for {
				_, err := c.Read(buf)
				if err != nil {
					t.Errorf("failed to read: %v", err)
				}

				msg := binary.LittleEndian.Uint64(buf)
				if prev != start && msg != prev+2 { // skip first message
					t.Errorf("expected: %d, got: %d", prev+2, msg)
				}
				prev = msg

				// Echo back the message + 1
				binary.LittleEndian.PutUint64(buf, msg+1)
				_, err = c.Write(buf)
				if err != nil {
					t.Errorf("failed to write: %v", err)
				}

				if prev >= 1000 { // exit condition
					return
				}
			}
		}

		wg.Add(2)
		go func() {
			defer wg.Done()
			pingPonger(stcConn)
		}()
		go func() {
			defer wg.Done()
			pingPonger(customConn)
		}()

		// start chain
		if _, err := stcConn.Write(binary.LittleEndian.AppendUint64(nil, start)); err != nil {
			t.Fatalf("failed to write: %v", err)
		}

		wg.Wait()
	})

	t.Run("racy read", func(t *testing.T) {
		data := make([]byte, 1<<20)
		rand.Read(data)
		stcConn, customConn := testPipe(t)

		var wg sync.WaitGroup

		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := copyBuffer(stcConn, bytes.NewReader(data)); err != nil {
				t.Errorf("failed to write to stcConn: %v", err)
			}
		}()

		customConn.SetReadDeadline(time.Now().Add(time.Millisecond))
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()

				b1 := make([]byte, 1024)
				b2 := make([]byte, 1024)
				for j := 0; j < 100; j++ {
					_, err := customConn.Read(b1)
					copy(b1, b2) // Mutate b1 to trigger potential race
					if err != nil {
						// timeout is expected here, as we set a short deadline
						if !errors.Is(err, context.DeadlineExceeded) {
							t.Error("failed to read from customConn:", err)
						}
						customConn.SetReadDeadline(time.Now().Add(time.Millisecond))
					}
				}
			}()
		}

		wg.Wait()
	})

	t.Run("racy write", func(t *testing.T) {
		l, stdConn, customConn, err := pipe()
		if err != nil {
			t.Fatalf("failed to create pipe: %v", err)
		}
		defer l.Close()

		var wg sync.WaitGroup

		copyDone := make(chan error)
		go func() {
			copyDone <- copyBuffer(io.Discard, stdConn)
		}()

		customConn.SetWriteDeadline(time.Now().Add(time.Millisecond))
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()

				b1 := make([]byte, 1024)
				b2 := make([]byte, 1024)
				for j := 0; j < 100; j++ {
					_, err := customConn.Write(b1)
					copy(b1, b2) // Mutate b1 to trigger potential race
					if err != nil {
						// timeout is expected here, as we set a short deadline
						if !errors.Is(err, context.DeadlineExceeded) {
							t.Error("failed to write to customConn:", err)
						}
						customConn.SetWriteDeadline(time.Now().Add(time.Millisecond))
					}
				}
			}()
		}

		wg.Wait()
		// close the connection to unblock the copyBuffer goroutine
		if err := stdConn.Close(); err != nil {
			t.Errorf("failed to close stdConn: %v", err)
		}

		if err := <-copyDone; err != nil {
			if !errors.Is(err, net.ErrClosed) {
				t.Errorf("unexpected error from copyBuffer: %v", err)
			}
		}
		if err := customConn.Close(); err != nil {
			t.Errorf("failed to close customConn: %v", err)
		}
	})

	t.Run("past timeout", func(t *testing.T) {
		_, customConn := testPipe(t)

		customConn.SetDeadline(time.Now().Add(-time.Millisecond))
		_, err := customConn.Write([]byte("hello"))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("expected deadline exceeded error, got: %v", err)
		}

		buf := make([]byte, 5)
		_, err = customConn.Read(buf)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("expected deadline exceeded error, got: %v", err)
		}
	})

	t.Run("close unblocks", func(t *testing.T) {
		_, customConn := testPipe(t)

		done := make(chan struct{})
		go func() {
			defer close(done)
			buf := make([]byte, 1024)
			for {
				_, err := customConn.Read(buf)
				if errors.Is(err, net.ErrClosed) {
					return
				}
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		}()

		time.Sleep(100 * time.Millisecond) // ensure goroutine is blocked
		if err := customConn.Close(); err != nil {
			t.Errorf("failed to close customConn: %v", err)
		}

		select {
		case <-done:
			return
		case <-time.After(1 * time.Second):
			t.Error("read goroutine did not exit after closing customConn")
		}
	})
}

func testPipe(t *testing.T) (net.Conn, net.Conn) {
	l, stcConn, customConn, err := pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	registerCleanup(t, stcConn, customConn, l)
	return stcConn, customConn
}

func pipe() (net.Listener, net.Conn, net.Conn, error) {
	localhost := &net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 0, // let the system choose a free port
	}
	llc := &udp.ListenConfig{}
	listener, err := llc.Listen(context.TODO(), localhost)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to listen UDP: %v", err)
	}

	ld := &platform.LocalDialer{}
	stdConn, err := ld.DialUDP(context.TODO(), listener.Addr().(*net.UDPAddr).Port)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to dial UDP: %w", err)
	}

	handshake := "hello"
	_, err = stdConn.Write([]byte(handshake))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to write to UDP connection: %w", err)
	}

	custom, err := listener.Accept()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to accept UDP connection: %w", err)
	}

	buf := make([]byte, len(handshake))
	n, err := custom.Read(buf)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to read from UDP connection: %w", err)
	}

	res := string(buf[:n])
	if res != handshake {
		return nil, nil, nil, fmt.Errorf("handshake failed, expected: %s, got: %s", handshake, res)
	}

	return listener, stdConn, custom, nil
}

// avoid stdConn to implement [io.WriterTo]
func copyBuffer(w io.Writer, r io.Reader) error {
	w = struct{ io.Writer }{w}
	r = struct{ io.Reader }{r}
	_, err := io.CopyBuffer(w, r, make([]byte, 1024))
	return err
}

func registerCleanup(t *testing.T, stdConn, custom net.Conn, listener net.Listener) {
	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			t.Errorf("failed to close listener: %v", err)
		}
		if err := stdConn.Close(); err != nil {
			t.Errorf("failed to close std conn: %v", err)
		}
		if err := custom.Close(); err != nil {
			t.Errorf("failed to close custom conn: %v", err)
		}
	})
}

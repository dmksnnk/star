package udp_test

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/dmksnnk/star/internal/platform/udp"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestUDPConn(t *testing.T) {
	// cannot use nettest.TestConn because it is UDP is datagram-oriented, not stream-oriented
	// and other part of the connection won't receive FIN packet.
	t.Run("simple", func(t *testing.T) {
		stdConn, customConn := testPipe(t)

		for i := range 10 {
			msg := []byte("hello " + strconv.Itoa(i))
			_, err := stdConn.Write(msg)
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
		stdConn, customConn := testPipe(t)
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
			pingPonger(stdConn)
		}()
		go func() {
			defer wg.Done()
			pingPonger(customConn)
		}()

		// start chain
		if _, err := stdConn.Write(binary.LittleEndian.AppendUint64(nil, start)); err != nil {
			t.Fatalf("failed to write: %v", err)
		}

		wg.Wait()
	})

	t.Run("racy read", func(t *testing.T) {
		data := make([]byte, 1<<20)
		rand.Read(data)
		stdConn, customConn := testPipe(t)

		var wg sync.WaitGroup

		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := copyBuffer(stdConn, bytes.NewReader(data)); err != nil {
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
						if !errors.Is(err, os.ErrDeadlineExceeded) {
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
		stdConn, customConn, err := udp.Pipe()
		if err != nil {
			t.Fatalf("failed to create pipe: %v", err)
		}

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
						if !errors.Is(err, os.ErrDeadlineExceeded) {
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
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("expected deadline exceeded error, got: %v", err)
		}

		buf := make([]byte, 5)
		_, err = customConn.Read(buf)
		if !errors.Is(err, os.ErrDeadlineExceeded) {
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
	stcConn, customConn, err := udp.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	registerCleanup(t, stcConn, customConn)
	return stcConn, customConn
}

// avoid stdConn to implement [io.WriterTo]
func copyBuffer(w io.Writer, r io.Reader) error {
	w = struct{ io.Writer }{w}
	r = struct{ io.Reader }{r}
	_, err := io.CopyBuffer(w, r, make([]byte, 1024))
	return err
}

func registerCleanup(t *testing.T, stdConn, custom net.Conn) {
	t.Cleanup(func() {
		if err := stdConn.Close(); err != nil {
			t.Errorf("failed to close std conn: %v", err)
		}
		if err := custom.Close(); err != nil {
			t.Errorf("failed to close custom conn: %v", err)
		}
	})
}

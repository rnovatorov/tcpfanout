package downstream

import (
	"log"
	"net"
	"sync"
	"time"

	"github.com/rnovatorov/tcpfanout/pkg/errs"
	"github.com/rnovatorov/tcpfanout/pkg/lognet"
	"github.com/rnovatorov/tcpfanout/pkg/streaming"
)

type ServerParams struct {
	ListenAddr   string
	Fanout       *streaming.Fanout
	WriteTimeout time.Duration
}

type Server struct {
	ServerParams
	addr     net.Addr
	started  chan struct{}
	stopped  chan struct{}
	once     sync.Once
	stopping chan struct{}
}

func StartServer(params ServerParams) (*Server, error, <-chan error) {
	srv := &Server{
		ServerParams: params,
		started:      make(chan struct{}),
		stopped:      make(chan struct{}),
		stopping:     make(chan struct{}),
	}
	errc := make(chan error, 1)
	go func() {
		defer close(srv.stopped)
		if err := srv.run(); err != nil {
			errc <- err
		}
	}()
	select {
	case <-srv.started:
		return srv, nil, errc
	case err := <-errc:
		return nil, err, nil
	}
}

func (srv *Server) Stop() {
	srv.once.Do(func() { close(srv.stopping) })
	<-srv.stopped
}

func (srv *Server) Addr() net.Addr {
	<-srv.started
	return srv.addr
}

func (srv *Server) run() error {
	var handlers sync.WaitGroup
	defer handlers.Wait()

	lsn, err := lognet.Listen("tcp", srv.ListenAddr)
	if err != nil {
		return err
	}
	defer lsn.Close()

	srv.addr = lsn.Addr()
	close(srv.started)

	conns, errc := srv.acceptConns(lsn)
	for id := 0; ; id++ {
		select {
		case <-srv.stopping:
			return errs.Stopping
		case err := <-errc:
			return err
		case conn := <-conns:
			handlers.Add(1)
			go func(id int) {
				defer handlers.Done()
				defer conn.Close()
				srv.handle(id, conn)
			}(id)
		}
	}
}

func (srv *Server) acceptConns(lsn net.Listener) (<-chan net.Conn, <-chan error) {
	conns := make(chan net.Conn)
	errc := make(chan error, 1)
	go func() {
		for {
			conn, err := lsn.Accept()
			if err != nil {
				errc <- err
				return
			}
			select {
			case conns <- conn:
			case <-srv.stopping:
				conn.Close()
				return
			}
		}
	}()
	return conns, errc
}

func (srv *Server) handle(id int, conn net.Conn) {
	s := &session{
		id:           id,
		conn:         conn,
		fanout:       srv.Fanout,
		writeTimeout: srv.WriteTimeout,
		stopping:     srv.stopping,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := s.run(); err != nil {
			log.Printf("error, downstream session-%d: %v", id, err)
		} else {
			log.Printf("info, stopped downstream session-%d", id)
		}
	}()
	select {
	case <-done:
	case <-srv.stopping:
	}
}

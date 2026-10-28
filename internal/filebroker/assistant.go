package filebroker

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/zhongwater123/A-NAS/internal/filebroker/wire"

	"github.com/zhongwater123/A-NAS/internal/accountid"
)

// AssistantConn is a connection to the signed-in member's assistant, handed
// out by the File Broker. It stops working when the member's session does.
type AssistantConn struct {
	net.Conn
	// Username is the member the broker found behind the session.
	Username string
	// Token is an access token of Hermes's own login to the instance.
	Token   string
	control *net.UnixConn
	close   sync.Once
}

// ConnectAssistant asks the File Broker for a connection to the assistant of
// the member signed in on the context's session. fresh asks for a new login,
// when the instance refused the last token.
func (c *Client) ConnectAssistant(ctx context.Context, fresh bool) (*AssistantConn, error) {
	token := accountid.SessionToken(ctx)
	if token == "" {
		return nil, fmt.Errorf("%w: no session for the assistant", wire.ErrUnavailable)
	}
	var dialer net.Dialer
	connection, err := dialer.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", wire.ErrUnavailable, err)
	}
	control := connection.(*net.UnixConn)
	stop := context.AfterFunc(ctx, func() { _ = control.Close() })
	defer stop()
	if err := wire.WriteMessage(control, wire.Request{Token: token, Op: wire.OpAssistant, Fresh: fresh}, nil); err != nil {
		_ = control.Close()
		return nil, fmt.Errorf("%w: %v", wire.ErrUnavailable, err)
	}
	var result wire.Response
	file, err := wire.ReadMessage(control, &result)
	if err == nil && result.Error != nil {
		err = result.Error
	}
	if err == nil && (file == nil || result.Assistant == nil) {
		err = fmt.Errorf("%w: no assistant connection returned", wire.ErrUnavailable)
	}
	if err != nil {
		wire.CloseFile(file)
		_ = control.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	instance, err := net.FileConn(file)
	wire.CloseFile(file)
	if err != nil {
		_ = control.Close()
		return nil, fmt.Errorf("%w: %v", wire.ErrUnavailable, err)
	}
	assistant := &AssistantConn{Conn: instance, Username: result.Assistant.Username, Token: result.Assistant.Token, control: control}
	go assistant.watch()
	return assistant, nil
}

// watch closes the connection when the broker ends it or goes away. The
// broker cuts the connection itself; this only releases it promptly.
func (a *AssistantConn) watch() {
	var result wire.Response
	file, _ := wire.ReadMessage(a.control, &result)
	wire.CloseFile(file)
	_ = a.Close()
}

// Close closes the connection and lets the broker forget it.
func (a *AssistantConn) Close() error {
	var err error
	a.close.Do(func() {
		err = a.Conn.Close()
		_ = a.control.Close()
	})
	return err
}

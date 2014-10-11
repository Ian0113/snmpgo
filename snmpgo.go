package snmpgo

import (
	"fmt"
	"math"
	"net"
	"time"
)

type SNMPArguments struct {
	Version         SNMPVersion   // SNMP version to use
	Network         string        // See net.Dial parameter (The default is `udp`)
	Address         string        // See net.Dial parameter
	Timeout         time.Duration // Request timeout (The default is 5sec)
	Retries         uint          // Number of retries (The default is `0`)
	MessageMaxSize  int           // Maximum size of an SNMP message (The default is `1400`)
	Community       string        // Community (V1 or V2c specific)
	UserName        string        // Security name (V3 specific)
	SecurityLevel   SecurityLevel // Security level (V3 specific)
	AuthPassword    string        // Authentication protocol pass phrase (V3 specific)
	AuthProtocol    AuthProtocol  // Authentication protocol (V3 specific)
	PrivPassword    string        // Privacy protocol pass phrase (V3 specific)
	PrivProtocol    PrivProtocol  // Privacy protocol (V3 specific)
	ContextEngineId string        // Context engine ID (V3 specific)
	ContextName     string        // Context name (V3 specific)
}

func (a *SNMPArguments) setDefault() {
	if a.Network == "" {
		a.Network = "udp"
	}
	if a.Timeout <= 0 {
		a.Timeout = timeoutDefault
	}
	if a.MessageMaxSize == 0 {
		a.MessageMaxSize = msgSizeDefault
	}
}

func (a *SNMPArguments) validate() error {
	if v := a.Version; v != V1 && v != V2c && v != V3 {
		return ArgumentError{
			Value:   v,
			Message: fmt.Sprintf("Unknown SNMP Version"),
		}
	}
	// RFC3412 Section 6
	if m := a.MessageMaxSize; (m != 0 && m < msgSizeMinimum) || m > math.MaxInt32 {
		return ArgumentError{
			Value: m,
			Message: fmt.Sprintf("MessageMaxSize is range %d..%d",
				msgSizeMinimum, math.MaxInt32),
		}
	}
	if a.Version == V3 {
		// RFC3414 Section 5
		if l := len(a.UserName); l < 1 || l > 32 {
			return ArgumentError{
				Value:   a.UserName,
				Message: "UserName length is range 1..32",
			}
		}
		if a.SecurityLevel > NoAuthNoPriv {
			// RFC3414 Section 11.2
			if len(a.AuthPassword) < 8 {
				return ArgumentError{
					Value:   a.AuthPassword,
					Message: "AuthPassword is at least 8 characters in length",
				}
			}
			if p := a.AuthProtocol; p != Md5 && p != Sha {
				return ArgumentError{
					Value:   a.AuthProtocol,
					Message: "Illegal AuthProtocol",
				}
			}
		}
		if a.SecurityLevel > AuthNoPriv {
			// RFC3414 Section 11.2
			if len(a.PrivPassword) < 8 {
				return ArgumentError{
					Value:   a.PrivPassword,
					Message: "PrivPassword is at least 8 characters in length",
				}
			}
			if p := a.PrivProtocol; p != Des && p != Aes {
				return ArgumentError{
					Value:   a.PrivProtocol,
					Message: "Illegal PrivProtocol",
				}
			}
		}
		if a.ContextEngineId != "" {
			_, err := engineIdToBytes(a.ContextEngineId)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *SNMPArguments) String() string {
	return escape(a)
}

type SNMP struct {
	args SNMPArguments
	mp   messageProcessing
	conn net.Conn
}

func (s *SNMP) Open() (err error) {
	if s.conn != nil {
		return
	}

	err = retry(int(s.args.Retries), func() error {
		conn, e := net.DialTimeout(s.args.Network, s.args.Address, s.args.Timeout)
		if e == nil {
			s.conn = conn
			s.mp = newMessageProcessing(s.args.Version)
		}
		return e
	})
	if err != nil {
		return
	}

	err = retry(int(s.args.Retries), func() error {
		return s.mp.Security().Discover(s)
	})
	if err != nil {
		s.Close()
		return
	}
	return
}

func (s *SNMP) Close() {
	if s.conn != nil {
		s.conn.Close()
		s.conn = nil
		s.mp = nil
	}
}

func (s *SNMP) GetRequest(oids []*Oid) (result Pdu, err error) {
	pdu := NewPduWithOids(s.args.Version, GetRequest, oids)

	retry(int(s.args.Retries), func() error {
		result, err = s.sendPdu(pdu)
		return err
	})
	return
}

func (s *SNMP) GetNextRequest(oids []*Oid) (result Pdu, err error) {
	pdu := NewPduWithOids(s.args.Version, GetNextRequest, oids)

	retry(int(s.args.Retries), func() error {
		result, err = s.sendPdu(pdu)
		return err
	})
	return
}

func (s *SNMP) GetBulkRequest(
	oids []*Oid, nonRepeaters, maxRepetitions int) (result Pdu, err error) {

	if s.args.Version < V2c {
		return nil, ArgumentError{
			Value:   s.args.Version,
			Message: "Unsupported SNMP Version",
		}
	}
	// RFC 3416 Section 3
	if nonRepeaters < 0 || nonRepeaters > math.MaxInt32 {
		return nil, ArgumentError{
			Value:   nonRepeaters,
			Message: fmt.Sprintf("NonRepeaters is range %d..%d", 0, math.MaxInt32),
		}
	}
	if maxRepetitions < 0 || maxRepetitions > math.MaxInt32 {
		return nil, ArgumentError{
			Value:   maxRepetitions,
			Message: fmt.Sprintf("NonRepeaters is range %d..%d", 0, math.MaxInt32),
		}
	}

	pdu := NewPduWithOids(s.args.Version, GetBulkRequest, oids)
	pdu.SetNonrepeaters(nonRepeaters)
	pdu.SetMaxRepetitions(maxRepetitions)

	retry(int(s.args.Retries), func() error {
		result, err = s.sendPdu(pdu)
		return err
	})
	return
}

func (s *SNMP) V2Trap(varBinds VarBinds) (err error) {
	if s.args.Version < V2c {
		return ArgumentError{
			Value:   s.args.Version,
			Message: "Unsupported SNMP Version",
		}
	}

	pdu := NewPduWithVarBinds(s.args.Version, SNMPTrapV2, varBinds)

	retry(int(s.args.Retries), func() error {
		_, err = s.sendPdu(pdu)
		return err
	})
	return
}

func (s *SNMP) sendPdu(pdu Pdu) (result Pdu, err error) {
	if err = s.Open(); err != nil {
		return
	}

	var sendMsg message
	sendMsg, err = s.mp.PrepareOutgoingMessage(s, pdu)
	if err != nil {
		return
	}

	var buf []byte
	buf, err = sendMsg.Marshal()
	if err != nil {
		return
	}

	s.conn.SetWriteDeadline(time.Now().Add(s.args.Timeout))
	_, err = s.conn.Write(buf)
	if !confirmedType(pdu.PduType()) || err != nil {
		return
	}

	size := s.args.MessageMaxSize
	if size < recvBufferSize {
		size = recvBufferSize
	}
	buf = make([]byte, size)
	s.conn.SetReadDeadline(time.Now().Add(s.args.Timeout))
	_, err = s.conn.Read(buf)
	if err != nil {
		return
	}

	result, err = s.mp.PrepareDataElements(s, sendMsg, buf)
	if result != nil && len(pdu.VarBinds()) != 0 {
		if err = s.checkPdu(result); err != nil {
			result = nil
		}
	}
	return
}

func (s *SNMP) checkPdu(pdu Pdu) (err error) {
	varBinds := pdu.VarBinds()
	if s.args.Version == V3 && pdu.PduType() == Report && len(varBinds) > 0 {
		oid := varBinds[0].Oid.String()
		rep := reportStatusOid(oid)
		err = ResponseError{
			Message: fmt.Sprintf("Received a report from the agent - %s(%s)", rep, oid),
			Detail:  fmt.Sprintf("Pdu - %s", pdu),
		}
		// perhaps the agent has rebooted after the previous communication
		if rep == usmStatsNotInTimeWindows {
			err = notInTimeWindowError{err.(ResponseError)}
		}
	}
	return
}

func (s *SNMP) String() string {
	if s.conn == nil {
		return fmt.Sprintf(`{"conn": false, "args": %s}`, s.args.String())
	} else {
		return fmt.Sprintf(`{"conn": true, "args": %s, "security": %s}`,
			s.args.String(), s.mp.Security().String())
	}
}

func NewSNMP(args SNMPArguments) (*SNMP, error) {
	if err := args.validate(); err != nil {
		return nil, err
	}
	args.setDefault()
	return &SNMP{args: args}, nil
}

package protonegotiate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"hypernet-node/hypernet/services/proxy/protomanager"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// ProtocolID — libp2p-идентификатор протокола согласования.
const ProtocolID = "/hypernet/negotiate/1.0.0"

// offer описывает список протоколов и транспортов, поддерживаемых одной стороной.
type offer struct {
	Protocols  []string `json:"protocols"`
	Transports []string `json:"transports,omitempty"` // предпочитаемые транспорты: "tcp", "ws", "grpc", "kcp"
}

// response описывает ответ на согласование протоколов и транспорта.
type response struct {
	Protocols       []string `json:"protocols"`
	Chosen          string   `json:"chosen"`
	ChosenTransport string   `json:"chosen_transport,omitempty"` // выбранный транспорт (если согласован)
}

// HandleStream обрабатывает входящий negotiation-поток на стороне ноды.
// Он читает список протоколов удалённой стороны, выбирает общий протокол
// с учётом приоритетов менеджера и отправляет ответ.
//
// Возвращает имя выбранного протокола или ошибку.
func HandleStream(ctx context.Context, s network.Stream, mgr *protomanager.Manager) (string, error) {
	defer s.Close()

	if mgr == nil {
		return "", fmt.Errorf("protocol manager is nil")
	}

	dec := json.NewDecoder(s)
	var off offer
	if err := dec.Decode(&off); err != nil {
		return "", fmt.Errorf("decode offer: %w", err)
	}

	localSupported := mgr.SupportedNames()
	// Приоритет порядка берём с клиентской стороны (off.Protocols),
	// а localSupported используем как фильтр поддерживаемых протоколов.
	chosen, ok := mgr.ChooseCommon(off.Protocols, localSupported)
	if !ok {
		resp := response{
			Protocols: localSupported,
			Chosen:    "",
		}
		if err := json.NewEncoder(s).Encode(&resp); err != nil {
			return "", fmt.Errorf("encode response (no common): %w", err)
		}
		return "", fmt.Errorf("no common protocol")
	}

	// Согласование транспорта: первый общий из списка клиента и списка сервера.
	chosenTransport := mgr.ChooseCommonTransport(off.Transports)

	resp := response{
		Protocols:       localSupported,
		Chosen:          chosen,
		ChosenTransport: chosenTransport,
	}
	if err := json.NewEncoder(s).Encode(&resp); err != nil {
		return "", fmt.Errorf("encode response: %w", err)
	}

	return chosen, nil
}

// ClientNegotiate инициирует negotiation-процедуру на стороне клиента.
// Возвращает выбранный протокол и (при согласовании) выбранный транспорт.
func ClientNegotiate(ctx context.Context, h host.Host, target peer.ID, mgr *protomanager.Manager) (chosenProto, chosenTransport string, err error) {
	if h == nil {
		return "", "", fmt.Errorf("host is nil")
	}
	if mgr == nil {
		return "", "", fmt.Errorf("protocol manager is nil")
	}

	stream, err := h.NewStream(ctx, target, ProtocolID)
	if err != nil {
		return "", "", fmt.Errorf("open negotiation stream: %w", err)
	}
	defer stream.Close()

	localSupported := mgr.SupportedNames()
	transports := mgr.SupportedTransports()
	if t := mgr.DefaultTransport(); t != "" {
		transports = append([]string{t}, transports...)
	}
	off := offer{
		Protocols:  localSupported,
		Transports: transports,
	}

	if err := json.NewEncoder(stream).Encode(&off); err != nil {
		return "", "", fmt.Errorf("encode offer: %w", err)
	}

	dec := json.NewDecoder(stream)
	var resp response
	if err := dec.Decode(&resp); err != nil {
		if err == io.EOF {
			return "", "", fmt.Errorf("decode response: unexpected EOF")
		}
		return "", "", fmt.Errorf("decode response: %w", err)
	}

	if resp.Chosen == "" {
		return "", "", fmt.Errorf("no common protocol (remote supported=%v)", resp.Protocols)
	}

	return resp.Chosen, resp.ChosenTransport, nil
}


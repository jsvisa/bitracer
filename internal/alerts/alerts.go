package alerts

import (
	"context"
	"log/slog"

	"github.com/jsvisa/bitracer/internal/notify"
	"github.com/jsvisa/bitracer/internal/store"
)

func NotifyCase(ctx context.Context, st *store.Store, caseID int64, msg notify.Message) {
	chs, err := st.ListCaseChannels(ctx, caseID)
	if err != nil {
		slog.Error("load channels failed", "case", caseID, "err", err)
		return
	}
	var ns []notify.Notifier
	for _, ch := range chs {
		n, err := notify.Build(ch.Type, ch.Config)
		if err != nil {
			slog.Error("bad channel config", "case", caseID, "channel", ch.ID, "err", err)
			continue
		}
		ns = append(ns, n)
	}
	notify.SendAll(ctx, ns, msg)
}

func Emit(ctx context.Context, st *store.Store, a store.Alert, msg notify.Message) error {
	a.Message = msg.Plain()
	if err := st.AddAlert(ctx, a); err != nil {
		return err
	}
	NotifyCase(ctx, st, a.CaseID, msg)
	return nil
}

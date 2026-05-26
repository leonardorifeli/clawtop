// Package merger combines per-machine Status payloads into a single view.
// Rate-limit windows are account-scoped on Anthropic's side, so we keep the
// freshest report. Token aggregations are summed across machines, with
// per-machine attribution preserved on the project list.
package merger

import (
	"sort"

	"github.com/leonardorifeli/clawtop/internal/domain"
)

type Merged struct {
	Machines     []MachineInfo
	TS           int64
	Session      domain.Window
	Week         domain.Window
	Limit        string
	Subscription string
	Window       string
	ByProject    []domain.Project
	ByModel      []domain.Model
	Hourly24h    []int64
}

type MachineInfo struct {
	Name string
	TS   int64
}

func Merge(parts []domain.Status) Merged {
	out := Merged{Hourly24h: make([]int64, 24)}
	if len(parts) == 0 {
		return out
	}

	// Pick freshest by TS for rate-limit / subscription / window labels.
	freshest := 0
	for i := 1; i < len(parts); i++ {
		if parts[i].TS > parts[freshest].TS {
			freshest = i
		}
	}
	f := parts[freshest]
	out.TS = f.TS
	out.Session = f.Session
	out.Week = f.Week
	out.Limit = f.Limit
	out.Subscription = f.Subscription
	out.Window = f.Window

	// Sum projects keyed on full path; sum models keyed on model id.
	projects := map[string]*domain.Project{}
	models := map[string]*domain.Model{}

	for _, p := range parts {
		out.Machines = append(out.Machines, MachineInfo{Name: p.Machine, TS: p.TS})

		for _, pj := range p.ByProject {
			key := pj.Path
			if key == "" {
				key = pj.Name
			}
			ex, ok := projects[key]
			if !ok {
				cp := pj
				projects[key] = &cp
				continue
			}
			ex.In += pj.In
			ex.Out += pj.Out
			ex.CacheR += pj.CacheR
			ex.CacheC += pj.CacheC
		}

		for _, m := range p.ByModel {
			ex, ok := models[m.Model]
			if !ok {
				cp := m
				models[m.Model] = &cp
				continue
			}
			ex.In += m.In
			ex.Out += m.Out
			ex.CacheR += m.CacheR
			ex.CacheC += m.CacheC
		}

		for i := 0; i < 24 && i < len(p.Hourly24h); i++ {
			out.Hourly24h[i] += p.Hourly24h[i]
		}
	}

	for _, v := range projects {
		out.ByProject = append(out.ByProject, *v)
	}
	sort.Slice(out.ByProject, func(i, j int) bool {
		return out.ByProject[i].Total() > out.ByProject[j].Total()
	})

	for _, v := range models {
		out.ByModel = append(out.ByModel, *v)
	}
	sort.Slice(out.ByModel, func(i, j int) bool {
		return out.ByModel[i].Total() > out.ByModel[j].Total()
	})

	sort.Slice(out.Machines, func(i, j int) bool { return out.Machines[i].Name < out.Machines[j].Name })
	return out
}

package store

import (
	"context"

	"oldwangvideo/internal/domain"
)

// Bootstrap 汇总应用首屏需要的供应商、模型、模板和近期记录。
func (s *Store) Bootstrap(ctx context.Context, recentLimit int) (domain.Bootstrap, error) {
	if recentLimit < 1 {
		recentLimit = 20
	}
	if recentLimit > 100 {
		recentLimit = 100
	}
	providers, err := s.ListProviders(ctx, true)
	if err != nil {
		return domain.Bootstrap{}, err
	}
	models, err := s.ListModels(ctx, domain.ModelFilter{})
	if err != nil {
		return domain.Bootstrap{}, err
	}
	templates, err := s.ListPromptTemplates(ctx, true)
	if err != nil {
		return domain.Bootstrap{}, err
	}
	recent, err := s.ListHistory(ctx, domain.HistoryFilter{
		PageRequest: domain.PageRequest{Page: 1, PageSize: recentLimit},
	})
	if err != nil {
		return domain.Bootstrap{}, err
	}
	favorites, err := s.ListFavorites(ctx, domain.PageRequest{Page: 1, PageSize: recentLimit})
	if err != nil {
		return domain.Bootstrap{}, err
	}
	return domain.Bootstrap{
		Providers:       providers,
		Models:          models,
		PromptTemplates: templates,
		ProviderPresets: domain.DefaultProviderPresets(),
		Capabilities:    domain.AllCapabilities(),
		RecentHistory:   recent.Items,
		Favorites:       favorites.Items,
	}, nil
}

// LoadBootstrap 是首屏数据查询的语义化别名。
func (s *Store) LoadBootstrap(ctx context.Context, recentLimit int) (domain.Bootstrap, error) {
	return s.Bootstrap(ctx, recentLimit)
}

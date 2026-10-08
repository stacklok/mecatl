package server

import "net/http"

func (*HTTPHandler) writeExecutionCatalog(w http.ResponseWriter, items []ExecutionTemplateInfo, revision string) {
	type row struct {
		Template struct {
			ID       string `json:"id"`
			Revision string `json:"revision"`
		} `json:"template"`
		Name         string            `json:"name"`
		Description  string            `json:"description"`
		DisplayToken string            `json:"display_token"`
		Extensions   map[string]string `json:"extensions"`
	}
	out := struct {
		Items             []row  `json:"items"`
		InventoryRevision string `json:"inventory_revision"`
	}{Items: make([]row, 0, len(items)), InventoryRevision: revision}
	for _, item := range items {
		record := row{Name: item.Name, Description: item.Description, DisplayToken: item.DisplayToken, Extensions: item.Extensions}
		record.Template.ID, record.Template.Revision = item.ID, item.Revision
		out.Items = append(out.Items, record)
	}
	writeJSON(w, http.StatusOK, out)
}

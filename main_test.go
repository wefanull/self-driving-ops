package main

import "testing"

func TestArbiterDecidir(t *testing.T) {
	tests := []struct {
		name     string
		analisis Analisis
		hist     Historial
		want     string
	}{
		{
			name:     "sin anomalia",
			analisis: Analisis{Anomaly: false, Severity: 0.1},
			want:     "sin_accion",
		},
		{
			name:     "anomalia cpu normal",
			analisis: Analisis{Anomaly: true, Type: "cpu_spike", Severity: 0.9},
			want:     "escalamiento_horizontal",
		},
		{
			name:     "exceso de acciones escala a humano",
			analisis: Analisis{Anomaly: true, Type: "cpu_spike", Severity: 0.9},
			hist: Historial{
				"web-01": {
					{Ciclo: 1, Accion: "escalamiento_horizontal"},
					{Ciclo: 2, Accion: "escalamiento_horizontal"},
				},
			},
			want: "escalar_a_humano",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := arbiterDecidir("web-01", 3, tt.analisis, tt.hist)
			if got.Accion != tt.want {
				t.Fatalf("esperaba accion %q, obtuvo %q", tt.want, got.Accion)
			}
		})
	}
}

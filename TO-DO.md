# 🗺️ Technical Roadmap & TO-DO

Este documento define la ruta crítica para estabilizar, optimizar y expandir las capacidades de **GoBalancer**. Sirve como bitácora de desarrollo y plan futuro.

---

## ✅ Historial de Logros (Completado)

### 🔴 Fase 1: Hardening & Correctitud (Core Stable)
- [x] **Protocol Agnostic PBR (UDP Support):** Soporte para reglas de tráfico gaming/VoIP (UDP) en `config.toml`.
- [x] **Graceful Shutdown & Cleanup:** Limpieza automática de reglas y rutas al detener el servicio (evita "basura" en el Kernel).
- [x] **Config Hot-Reload Atómico:** Validación de seguridad antes de recargar configuración con `SIGHUP` (Implementado con *Dry-Run*).
- [x] **Kernel Route Rehydration:** (CRÍTICO) Restauración automática de `Host Routes` (/32) y tablas dedicadas tras recuperación de interfaz (Fix `ResetInterface`).

### 🟠 Fase 2: Observabilidad & Operaciones (Visibilidad)
- [x] **Status Dump File (`/run/gobalancer/status.json`):** Exportación de estado en tiempo real para integraciones.
- [x] **CLI de Control (`gobalancer-ctl`):** Herramienta de terminal para ver estado y monitorización en vivo (`-w`).
- [x] **Structured Logging (Zerolog):** Migración a logs estructurados JSON (Prod) y Pretty-Print (Dev).
- [x] **Netlink Idempotency:** Optimización crítica para evitar escrituras innecesarias en el Kernel (reducción de CPU/Syscalls).
- [x] **Parallel Probing:** Monitorización concurrente (ICMP+TCP en paralelo) para mediciones de latencia precisas sin bloqueo.
- [x] **Conntrack Flushing:** Limpieza de conexiones "zombis" cuando una WAN cae para forzar el failover inmediato.

---

## 🚀 Roadmap Activo (Próximos Pasos)

### 🟡 Fase 3: Robustez y Trazabilidad (Prioridad Alta)
*Objetivo: Que la aplicación "hable" claro sobre sus decisiones y soporte entornos hostiles (DHCP/Carga).*

- [x] **Dynamic Gateway Discovery (DHCP Support):**
    - [x] *Problema:* Gateways estáticos en `config.toml` rompen si el ISP cambia la IP (muy común en 4G/Starlink).
    - [x] *Solución:* Permitir `gateway = "auto"`. Usar syscalls para preguntar al Kernel "¿quién es el gateway actual de `eth0`?".
- [x] **Notification Hooks (Alerting):**
    - [x] *Necesidad:* Saber si el internet cae sin mirar la consola.
    - [x] *Implementación:* Ejecutar scripts (`on_event_script`) inyectando variables de entorno (`GOBALANCER_STATUS`) ante eventos UP/DOWN.
- [ ] **Runtime Profiling (pprof):** (NEXT TARGET 🎯)
    - [ ] *Objetivo:* Detectar fugas de memoria y cuellos de botella en CPU antes de escalar.
    - [ ] *Implementación:* Exponer servidor HTTP opcional en `localhost:6060/debug/pprof` para ver en tiempo real dónde gasta recursos la aplicación (flamegraphs).
- [ ] **Scalable PBR (IPSet Support):** (NUEVO)
    - [ ] *Objetivo:* Manejar miles de reglas (listas de bloqueo/VPN) sin matar la CPU (O(1) vs O(N)).
    - [ ] *Implementación:* Integrar `ipset` nativo y lectura de archivos `.txt` externos en `config.toml`.
    - [ ] **⚠️ RECORDATORIO:** Actualizar README con la sintaxis de `[[ipsets]]` y archivos externos.
- [ ] **Decision Traceability (Logic Audit):**
    - [ ] *Objetivo:* "Saber el recorrido de la aplicación".
    - [ ] *Implementación:* Añadir un modo `--trace` que loguee la "Matriz de Decisión": por qué se eligió ruta A sobre ruta B (ej: "Ruta A descartada por SLA > 150ms").

### 🔵 Fase 4: Calidad de Red (QoS & Tuning)
*Mejorar la experiencia de usuario, no solo la conectividad.*

- [x] **Sticky Sessions (Hash Policy):**
    - [x] *Problema:* Bancos y HTTPS cierran sesión si la IP pública cambia en cada petición.
    - [x] *Solución:* Configurar `fib_multipath_hash_policy` del Kernel a L4 (Flujo) para garantizar persistencia de sesión.
- [ ] **Smart QoS (Anti-Bufferbloat):**
    - [ ] *Problema:* Descargar un archivo satura el enlace y el ping sube a 500ms.
    - [ ] *Solución:* Orquestar `tc` (Traffic Control) para activar algoritmos **CAKE** o **FQ_CODEL** en las WANs automáticamente.
- [ ] **DNS Integration:**
    - [ ] Gestionar una instancia local de `dnsmasq` para evitar usar los DNS del ISP caído.

### 🟣 Fase 5: API & Control Dinámico
*Gestión remota sin reiniciar.*

- [ ] **Unix Socket Control API:**
    - [ ] Servidor ligero en `/var/run/gobalancer.sock`.
    - [ ] Comandos para `gobalancer-ctl`: `disable interface <name>`, `force failover`.
- [ ] **Systemd Watchdog:**
    - [ ] Integración nativa (`sd_notify`) para que Systemd reinicie el proceso si se congela.

### 🟢 Fase 6: Futuro & Rendimiento Extremo
- [ ] **IPv6 Support:** Stack dual completo.
- [ ] **eBPF Monitoring (XDP):** Reemplazar el Pinger de espacio de usuario por código en Kernel para latencia de microsegundos.
- [ ] **Custom Health Checks:** Soporte para HTTP (`GET /health`) y DNS Queries como mecanismo de check.

---

## 🐛 Deuda Técnica & Mantenimiento
- [ ] **Unit Tests Coverage:** Aumentar cobertura en el paquete `routing` (actualmente difícil de testear por dependencia del Kernel). Mockear `netlink`.
- [ ] **Documentation:** Crear diagrama de arquitectura (Mermaid) para el README explicando el flujo de decisión.

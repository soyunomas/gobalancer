# 🗺️ Technical Roadmap & TO-DO

Este documento define la ruta crítica para estabilizar, optimizar y expandir las capacidades de **GoBalancer**.

---

## 🔴 Fase 1: Hardening & Correctitud (Prioridad Inmediata)
*El objetivo es evitar "comportamientos groseros" del software (dejando basura en el kernel) y soportar tráfico real (UDP).*

- [x] **Protocol Agnostic PBR (UDP Support):**
    - [x] *Problema:* Actualmente `ip rule` con puerto fuerza `IPPROTO_TCP`. VoIP y Gaming (UDP) no funcionan con reglas de puerto.
    - [x] *Solución:* Añadir campo `protocol` ("tcp", "udp") en `config.toml` y pasarlo a `netlink.Rule`.
- [x] **Graceful Shutdown & Cleanup:**
    - [x] *Problema:* Al detener el servicio, las reglas `ip rule` y las tablas de enrutamiento (ID 100+) se quedan "huérfanas" en el Kernel.
    - [x] *Solución:* Implementar método `manager.Cleanup()` que se ejecute en `SIGTERM/SIGINT` para borrar reglas creadas por nosotros.
- [x] **Config Hot-Reload Atómico:**
    - [x] *Problema:* `SIGHUP` recarga la config, pero si la nueva config tiene errores, el servicio podría quedar en un estado inconsistente.
    - [x] *Solución:* Validar la nueva configuración en memoria *antes* de aplicarla. Si falla, mantener la anterior y loguear error.

---

## 🟠 Fase 2: Observabilidad & Operaciones (Corto Plazo)
*El sistema es una "caja negra". Necesitamos saber qué está pasando sin leer logs crudos.*

- [x] **Status Dump File (`/run/gobalancer/status.json`):**
    - [x] Generar un archivo JSON cada 5 segundos con el estado actual (Interfaces UP/DOWN, Latencia, Reglas activas).
    - [x] Permitir a herramientas externas (Zabbix, Scripts, Dashboard web) leer el estado sin sockets complejos.
- [ ] **CLI de Control (`gobalancer-ctl`):**
    - [ ] Crear un pequeño binario que lea el `status.json` y lo muestre bonito en terminal.
    - [ ] Comandos: `gobalancer-ctl status`, `gobalancer-ctl config-check`.
- [ ] **Log Rotation / Leveling:**
    - [ ] Pasar de `log.Println` a una librería estructurada (ej: `slog` o `zerolog`).
    - [ ] Permitir nivel `DEBUG` para ver cada cambio de ruta y nivel `INFO` para producción.

---

## 🟡 Fase 3: Networking Avanzado (Medio Plazo)
*Funcionalidades para entornos corporativos o datacenters complejos.*

- [ ] **IPv6 Support:**
    - [ ] Actualmente el sistema es IPv4-only. El mundo se mueve a v6.
    - [ ] Duplicar lógica de `netlink` y `iptables` (usando `ip6tables`) para soportar stack dual.
- [ ] **DNS Load Balancing / Integration:**
    - [ ] *Problema:* Si se cae la WAN principal, los DNS de ese ISP podrían dejar de responder.
    - [ ] *Solución:* Integración opcional para reescribir `/etc/resolv.conf` o gestionar una instancia local de `dnsmasq` que apunte a DNS agnósticos (8.8.8.8, 1.1.1.1).
- [ ] **Custom Health Checks:**
    - [ ] Permitir checks HTTP (`GET /health` espera 200 OK) además de Ping/TCP. Útil para detectar portales cautivos o proxies transparentes caídos.

---

## 🟢 Fase 4: Rendimiento Extremo & Futuro (Largo Plazo)
*Optimizaciones para throughputs de 10Gbps+ o latencias de High Frequency Trading.*

- [ ] **eBPF Monitoring (XDP):**
    - [ ] Reemplazar el Pinger actual (User Space) por un programa eBPF en el Kernel que cuente paquetes y mida latencia sin context switching.
- [x] **Connection Tracking Flushing:**
    - [x] Cuando una WAN cae y vuelve, las conexiones establecidas a veces se quedan "pegadas" a la ruta muerta (Blackhole).
    - [x] Implementar borrado selectivo de `conntrack` (`conntrack -D -d <IP_WAN_CAIDA>`) al detectar failover.

---

## 🐛 Known Bugs / Deuda Técnica
- [x] **Dependencia Netlink:** Actualizar a v1.3.0+ para soporte de `RulePortRange`. *(Solucionado en build reciente)*.
- [x] **Race Conditions:** Revisar si `UpdateRoutes` puede ser llamado concurrentemente por múltiples monitores. *(Solucionado: Centralizado en loop único en main.go)*.


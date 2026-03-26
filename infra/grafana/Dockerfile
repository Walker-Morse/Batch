FROM grafana/grafana:10.4.3

USER root

# Replace Grafana nav bar logo with Morse logo
COPY grafana_icon.svg /usr/share/grafana/public/img/grafana_icon.svg

# Replace favicon (32x32)
COPY fav32.png /usr/share/grafana/public/img/fav32.png

# Replace apple touch icon
COPY apple-touch-icon.png /usr/share/grafana/public/img/apple-touch-icon.png

USER grafana

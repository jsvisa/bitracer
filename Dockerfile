FROM alpine:3.20
# chromium + noto fonts: the notify case-graph snapshot renders the
# dashboard in headless Chrome (apk needs network at image build time)
RUN adduser -D bitracer && \
    apk add --no-cache chromium font-noto
COPY build/bitracer /usr/local/bin/bitracer
COPY web/dist /web/dist
ENV BITRACER_WEB_DIR=/web/dist
USER bitracer
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/bitracer"]
CMD ["run"]

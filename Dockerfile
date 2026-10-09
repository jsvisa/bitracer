FROM alpine:3.20
RUN adduser -D bitracer
COPY build/bitracer /usr/local/bin/bitracer
COPY web/dist /web/dist
ENV BITRACER_WEB_DIR=/web/dist
USER bitracer
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/bitracer"]
CMD ["run"]

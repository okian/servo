# Built with the repo root as context, not this module's own directory —
# see docs/tutorial/19-running-and-deployment.md for why: this module's
# go.mod replaces github.com/okian/servo/v3 with a local path (../.., the
# servo repo itself), which only resolves if that path is actually present
# in the build context. A real project with a real, published dependency
# wouldn't need this; it's specific to this tutorial living inside servo's
# own repo.
FROM golang:1.27 AS build
WORKDIR /src
COPY . .
WORKDIR /src/examples/tutorial
RUN CGO_ENABLED=0 go build -o /out/orders ./cmd/orders

# The :nonroot variant runs as an unprivileged UID instead of root — free to
# switch to since both of this service's ports (8080, 8081) are well above
# 1024, the range only root can bind. Nothing else about the image changes.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/orders /orders
EXPOSE 8080 8081
ENTRYPOINT ["/orders"]

#
# First stage:
# Building a frontend.
#
# The output is platform-independent JS/CSS, so always build on the native
# build platform. Running node under emulation (e.g. building linux/amd64 on
# an arm64 Mac) is slow enough that yarn requests time out.
#

FROM --platform=$BUILDPLATFORM node:22-alpine AS frontend

# Move to a working directory (/static).
WORKDIR /static

# https://stackoverflow.com/questions/69692842/error-message-error0308010cdigital-envelope-routinesunsupported
ENV NODE_OPTIONS=--openssl-legacy-provider

# Install dependencies first so this layer is cached until they change.
COPY ui/package.json ui/yarn.lock ./
RUN yarn install --frozen-lockfile --network-timeout 600000

# Copy the rest of the ./ui folder and build.
COPY ui .
RUN yarn build

#
# Second stage:
# Building a backend.
#
# Cross-compile natively for the target platform instead of emulating it.
#

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS backend
ARG TARGETOS=linux
ARG TARGETARCH=amd64

# CA certificates for verifying TLS connections to redis (copied into the
# scratch image below).
RUN apk add --no-cache ca-certificates

# Move to a working directory (/build).
WORKDIR /build

# Copy and download dependencies.
COPY go.mod go.sum ./
RUN go mod download

# Copy a source code to the container.
COPY . .

# Copy frontend static files from /static to the root folder of the backend container.
COPY --from=frontend ["/static/build", "ui/build"]

# Set necessary environmet variables needed for the image and build the server.
ENV CGO_ENABLED=0

# Run go build (with ldflags to reduce binary size).
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -o asynqmon ./cmd/asynqmon

#
# Third stage:
# Creating and running a new scratch container with the backend binary.
#

FROM scratch

# Copy CA certificates and the binary from the backend stage.
COPY --from=backend ["/etc/ssl/certs/ca-certificates.crt", "/etc/ssl/certs/"]
COPY --from=backend ["/build/asynqmon", "/"]

# Command to run when starting the container.
ENTRYPOINT ["/asynqmon"]

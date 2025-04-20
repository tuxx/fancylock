# Use a Debian base image with build tools
FROM debian:bookworm-slim AS builder

# Install build essentials, git, nasm, static libx264, AND ca-certificates
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
    build-essential \
    git \
    nasm \
    pkg-config \
    libx264-dev \
    ca-certificates \
    # Add other static dev libraries here if needed in the future (e.g., libvpx-dev for VP9 static)
    && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

# Set a working directory
WORKDIR /build

# Clone ffmpeg source code (use a specific tag/commit for reproducibility)
ARG FFMPEG_VERSION=n6.1.1 # Or use "master" for latest git
RUN git clone --depth 1 --branch ${FFMPEG_VERSION} https://git.ffmpeg.org/ffmpeg.git ffmpeg
WORKDIR /build/ffmpeg

# Configure ffmpeg for static build (based on your requirements)
RUN ./configure \
      --pkg-config-flags="--static" \
      --extra-libs="-lpthread -lm" \
      --enable-gpl \
      --enable-libx264 \
      --disable-programs \
      --enable-ffmpeg \
      --disable-everything \
      --enable-demuxer=mov,mkv,matroska \
      --enable-decoder=h264,hevc,vp9 \
      --enable-filter=scale,fps \
      --enable-protocol=file,pipe \
      --enable-encoder=rawvideo \
      --enable-muxer=null,rawvideo \
      --disable-shared \
      --enable-static

# Build ffmpeg
RUN make -j$(nproc)

# Strip the binary to reduce size
RUN strip ffmpeg

# --- Final Stage (Optional but good practice) ---
# Copy just the stripped binary to a minimal image
FROM scratch

# Copy the static ffmpeg binary from the builder stage
COPY --from=builder /build/ffmpeg/ffmpeg /ffmpeg

# Default command (optional, just indicates what the image contains)
CMD ["/ffmpeg"]

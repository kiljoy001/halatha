// import { dbg_assert } from "../log.js";

/**
 * Screen adapter for use with OffscreenCanvas (e.g., in Web Workers).
 * Text and graphics modes render to the OffscreenCanvas.
 * No DOM dependencies.
 *
 * @constructor
 * @param {OffscreenCanvas} canvas - The OffscreenCanvas to render graphics to
 * @param {Function} screen_fill_buffer - Callback to request buffer fill for graphical mode
 */
export function OffscreenScreenAdapter(canvas, screen_fill_buffer)
{
    console.assert(canvas, "OffscreenCanvas must be provided");
    console.assert(screen_fill_buffer, "screen_fill_buffer callback must be provided");

    this.screen_fill_buffer = screen_fill_buffer;

    const use_webgl = typeof self !== "undefined" && self.HALATHA_WEBGL_SCREEN === true;

    var
        current_canvas = canvas,
        gl_renderer = use_webgl ? makeWebGLRenderer(canvas) : null,
        backing_canvas = gl_renderer ? new OffscreenCanvas(canvas.width || 1, canvas.height || 1) : canvas,
        graphic_context = backing_canvas.getContext("2d", { alpha: false }),

        /** @type {number} */
        cursor_row = 0,

        /** @type {number} */
        cursor_col = 0,

        graphical_mode_width = 0,
        graphical_mode_height = 0,

        // are we in graphical mode now?
        is_graphical = false,

        // Index 0: ASCII code
        // Index 1: Flags
        // Index 2: Background color
        // Index 3: Foreground color
        text_mode_data,
        text_mode_dirty,

        // number of columns
        text_mode_width = 0,

        // number of rows
        text_mode_height = 0,

        text_mode_font_width = 9,
        text_mode_font_height = 16,
        text_charmap,
        cursor_start = 0,
        cursor_end = 15,
        cursor_enabled = false,

        // render loop state
        timer_id = 0,
        paused = false,

        // Update interval in ms (roughly 60fps)
        update_interval = 16;

    const FLAG_BLINKING = 0x01;
    const FLAG_FONT_PAGE_B = 0x02;

    // Expose flags for API compatibility
    this.FLAG_BLINKING = FLAG_BLINKING;
    this.FLAG_FONT_PAGE_B = FLAG_FONT_PAGE_B;

    const TEXT_BUF_COMPONENT_SIZE = 4;
    const CHARACTER_INDEX = 0;

    this.init = function()
    {
        this.set_size_text(80, 25);
        this.timer();
    };

    this.put_char = function(row, col, chr, flags, bg_color, fg_color)
    {
        // dbg_assert(row >= 0 && row < text_mode_height);
        // dbg_assert(col >= 0 && col < text_mode_width);
        // dbg_assert(chr >= 0 && chr < 0x100);

        const p = TEXT_BUF_COMPONENT_SIZE * (row * text_mode_width + col);

        text_mode_data[p + CHARACTER_INDEX] = chr;
        text_mode_data[p + 1] = flags;
        text_mode_data[p + 2] = bg_color;
        text_mode_data[p + 3] = fg_color;
        text_mode_dirty[row] = 1;
    };

    this.timer = function()
    {
        timer_id = setTimeout(() => this.update_screen(), update_interval);
    };

    this.update_screen = function()
    {
        if(paused)
        {
            this.timer();
            return;
        }

        if(is_graphical)
        {
            this.screen_fill_buffer();
        }
        else
        {
            this.update_text();
        }
        this.timer();
    };

    this.update_text = function()
    {
        for(let row = 0; row < text_mode_height; row++)
        {
            if(text_mode_dirty[row])
            {
                this.text_update_row(row);
                text_mode_dirty[row] = 0;
            }
        }
        if(gl_renderer)
        {
            gl_renderer.present(backing_canvas);
        }
    };

    this.destroy = function()
    {
        if(timer_id)
        {
            clearTimeout(timer_id);
            timer_id = 0;
        }
    };

    this.pause = function()
    {
        paused = true;
    };

    this.continue = function()
    {
        paused = false;
    };

    this.set_mode = function(graphical)
    {
        is_graphical = graphical;
        if(!is_graphical)
        {
            this.resize_text_canvas();
            text_mode_dirty.fill(1);
        }
    };

    this.set_font_bitmap = function(height, width_9px, width_dbl, copy_8th_col, bitmap, bitmap_changed)
    {
        const width = width_dbl ? 16 : width_9px ? 9 : 8;
        if(height !== text_mode_font_height || width !== text_mode_font_width || bitmap_changed)
        {
            text_mode_font_height = height || 16;
            text_mode_font_width = width;
            if(!is_graphical)
            {
                this.resize_text_canvas();
            }
            text_mode_dirty.fill(1);
        }
    };

    this.set_font_page = function(page_a, page_b)
    {
        text_mode_dirty.fill(1);
    };

    this.clear_screen = function()
    {
        if(is_graphical && gl_renderer)
        {
            gl_renderer.clear();
            return;
        }
        graphic_context.fillStyle = "#000";
        graphic_context.fillRect(0, 0, current_canvas.width, current_canvas.height);
        if(text_mode_dirty)
        {
            text_mode_dirty.fill(1);
        }
    };

    /**
     * @param {number} cols
     * @param {number} rows
     */
    this.set_size_text = function(cols, rows)
    {
        if(cols === text_mode_width && rows === text_mode_height)
        {
            return;
        }

        text_mode_data = new Int32Array(cols * rows * TEXT_BUF_COMPONENT_SIZE);
        text_mode_dirty = new Uint8Array(rows);
        text_mode_dirty.fill(1);
        text_mode_width = cols;
        text_mode_height = rows;
        if(!is_graphical)
        {
            this.resize_text_canvas();
        }
    };

    this.resize_text_canvas = function()
    {
        current_canvas.width = text_mode_width * text_mode_font_width;
        current_canvas.height = text_mode_height * text_mode_font_height;
        backing_canvas.width = current_canvas.width;
        backing_canvas.height = current_canvas.height;
        graphic_context = backing_canvas.getContext("2d", { alpha: false });
        graphic_context.imageSmoothingEnabled = false;
        graphic_context.textBaseline = "top";
        graphic_context.font = text_mode_font_height + "px monospace";
    };

    this.set_size_graphical = function(width, height, buffer_width, buffer_height)
    {
        graphical_mode_width = width;
        graphical_mode_height = height;

        current_canvas.width = width;
        current_canvas.height = height;
        backing_canvas.width = width;
        backing_canvas.height = height;

        // graphic_context must be reconfigured whenever canvas is resized
        graphic_context = backing_canvas.getContext("2d", { alpha: false });
        graphic_context.imageSmoothingEnabled = false;
    };

    this.set_scale = function(s_x, s_y)
    {
        // No-op: scaling is handled externally for OffscreenCanvas
    };

    this.set_charmap = function(charmap)
    {
        text_charmap = charmap;
        text_mode_dirty.fill(1);
    };

    this.update_cursor_scanline = function(start, end, enabled)
    {
        cursor_start = start;
        cursor_end = end;
        cursor_enabled = enabled;
        if(cursor_row < text_mode_height)
        {
            text_mode_dirty[cursor_row] = 1;
        }
    };

    this.update_cursor = function(row, col)
    {
        if(cursor_row < text_mode_height)
        {
            text_mode_dirty[cursor_row] = 1;
        }
        cursor_row = row;
        cursor_col = col;
        if(cursor_row < text_mode_height)
        {
            text_mode_dirty[cursor_row] = 1;
        }
    };

    this.text_update_row = function(row)
    {
        const y = row * text_mode_font_height;
        for(let col = 0; col < text_mode_width; col++)
        {
            const p = TEXT_BUF_COMPONENT_SIZE * (row * text_mode_width + col);
            const x = col * text_mode_font_width;
            const chr = text_mode_data[p + CHARACTER_INDEX];
            const bg = text_mode_data[p + 2];
            const fg = text_mode_data[p + 3];

            graphic_context.fillStyle = color(bg);
            graphic_context.fillRect(x, y, text_mode_font_width, text_mode_font_height);
            graphic_context.fillStyle = color(fg);
            graphic_context.fillText(charFor(chr), x, y);
        }

        if(cursor_enabled && row === cursor_row && cursor_col < text_mode_width)
        {
            graphic_context.fillStyle = color(text_mode_data[TEXT_BUF_COMPONENT_SIZE * (row * text_mode_width + cursor_col) + 3]);
            graphic_context.fillRect(
                cursor_col * text_mode_font_width,
                y + Math.min(cursor_start, text_mode_font_height - 1),
                text_mode_font_width,
                Math.max(1, Math.min(cursor_end, text_mode_font_height - 1) - cursor_start + 1)
            );
        }
    };

    this.update_buffer = function(layers)
    {
        if(gl_renderer && is_graphical)
        {
            gl_renderer.update(layers);
            return;
        }
        for(const layer of layers)
        {
            graphic_context.putImageData(
                layer.image_data,
                layer.screen_x - layer.buffer_x,
                layer.screen_y - layer.buffer_y,
                layer.buffer_x,
                layer.buffer_y,
                layer.buffer_width,
                layer.buffer_height
            );
        }
    };

    this.get_text_screen = function()
    {
        var screen = [];

        for(var i = 0; i < text_mode_height; i++)
        {
            screen.push(this.get_text_row(i));
        }

        return screen;
    };

    this.get_text_row = function(row)
    {
        let result = "";

        for(let x = 0; x < text_mode_width; x++)
        {
            const index = (row * text_mode_width + x) * TEXT_BUF_COMPONENT_SIZE;
            const character = text_mode_data[index + CHARACTER_INDEX];
            result += charFor(character);
        }

        return result;
    };

    /**
     * Get the underlying canvas for transferring or drawing elsewhere
     * @returns {OffscreenCanvas}
     */
    this.get_canvas = function()
    {
        return current_canvas;
    };

    /**
     * Change the canvas at runtime
     * @param {OffscreenCanvas} new_canvas - The new OffscreenCanvas to render to
     */
    this.set_canvas = function(new_canvas)
    {
        console.assert(new_canvas, "OffscreenCanvas must be provided");

        current_canvas = new_canvas;
        gl_renderer = use_webgl ? makeWebGLRenderer(new_canvas) : null;
        backing_canvas = gl_renderer ? new OffscreenCanvas(new_canvas.width || 1, new_canvas.height || 1) : new_canvas;
        graphic_context = backing_canvas.getContext("2d", { alpha: false });

        // Apply current dimensions to new canvas
        if(is_graphical && graphical_mode_width && graphical_mode_height)
        {
            current_canvas.width = graphical_mode_width;
            current_canvas.height = graphical_mode_height;
            backing_canvas.width = graphical_mode_width;
            backing_canvas.height = graphical_mode_height;
        }
        else
        {
            this.resize_text_canvas();
            text_mode_dirty.fill(1);
        }

        graphic_context.imageSmoothingEnabled = false;
    };

    function makeWebGLRenderer(canvas)
    {
        const gl = canvas.getContext("webgl", {
            alpha: false,
            antialias: false,
            depth: false,
            preserveDrawingBuffer: true,
            premultipliedAlpha: false,
            stencil: false,
        });
        if(!gl)
        {
            return null;
        }

        const vertex = compile(gl.VERTEX_SHADER, `
            attribute vec2 a_pos;
            attribute vec2 a_uv;
            varying vec2 v_uv;
            void main() {
                gl_Position = vec4(a_pos, 0.0, 1.0);
                v_uv = a_uv;
            }
        `);
        const fragment = compile(gl.FRAGMENT_SHADER, `
            precision mediump float;
            varying vec2 v_uv;
            uniform sampler2D u_tex;
            void main() {
                gl_FragColor = texture2D(u_tex, v_uv);
            }
        `);
        if(!vertex || !fragment)
        {
            return null;
        }

        const program = gl.createProgram();
        gl.attachShader(program, vertex);
        gl.attachShader(program, fragment);
        gl.linkProgram(program);
        if(!gl.getProgramParameter(program, gl.LINK_STATUS))
        {
            console.warn("OffscreenScreenAdapter: WebGL link failed", gl.getProgramInfoLog(program));
            return null;
        }

        const posLoc = gl.getAttribLocation(program, "a_pos");
        const uvLoc = gl.getAttribLocation(program, "a_uv");
        const samplerLoc = gl.getUniformLocation(program, "u_tex");
        const vertexBuffer = gl.createBuffer();
        const texture = gl.createTexture();
        const verts = new Float32Array(24);

        gl.useProgram(program);
        gl.uniform1i(samplerLoc, 0);
        gl.bindTexture(gl.TEXTURE_2D, texture);
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.NEAREST);
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.NEAREST);
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
        gl.pixelStorei(gl.UNPACK_FLIP_Y_WEBGL, false);
        gl.pixelStorei(gl.UNPACK_PREMULTIPLY_ALPHA_WEBGL, false);

        function compile(type, source)
        {
            const shader = gl.createShader(type);
            gl.shaderSource(shader, source);
            gl.compileShader(shader);
            if(!gl.getShaderParameter(shader, gl.COMPILE_STATUS))
            {
                console.warn("OffscreenScreenAdapter: WebGL shader failed", gl.getShaderInfoLog(shader));
                return null;
            }
            return shader;
        }

        function ndcX(x)
        {
            return x / canvas.width * 2 - 1;
        }

        function ndcY(y)
        {
            return 1 - y / canvas.height * 2;
        }

        return {
            clear()
            {
                gl.viewport(0, 0, canvas.width, canvas.height);
                gl.clearColor(0, 0, 0, 1);
                gl.clear(gl.COLOR_BUFFER_BIT);
            },
            present(source)
            {
                gl.viewport(0, 0, canvas.width, canvas.height);
                this.drawSource(source, 0, 0, source.width, source.height, 0, 0, canvas.width, canvas.height);
                gl.flush();
            },
            update(layers)
            {
                gl.viewport(0, 0, canvas.width, canvas.height);

                for(const layer of layers)
                {
                    const img = layer.image_data;
                    this.drawSource(
                        img,
                        layer.buffer_x,
                        layer.buffer_y,
                        layer.buffer_width,
                        layer.buffer_height,
                        layer.screen_x,
                        layer.screen_y,
                        layer.buffer_width,
                        layer.buffer_height
                    );
                }
                gl.flush();
            },
            drawSource(source, sx, sy, sw, sh, dx, dy, dw, dh)
            {
                const x0 = dx;
                const y0 = dy;
                const x1 = dx + dw;
                const y1 = dy + dh;
                const u0 = sx / source.width;
                const v0 = sy / source.height;
                const u1 = (sx + sw) / source.width;
                const v1 = (sy + sh) / source.height;

                gl.useProgram(program);
                gl.activeTexture(gl.TEXTURE0);
                gl.bindTexture(gl.TEXTURE_2D, texture);
                gl.bindBuffer(gl.ARRAY_BUFFER, vertexBuffer);
                gl.enableVertexAttribArray(posLoc);
                gl.enableVertexAttribArray(uvLoc);
                gl.vertexAttribPointer(posLoc, 2, gl.FLOAT, false, 16, 0);
                gl.vertexAttribPointer(uvLoc, 2, gl.FLOAT, false, 16, 8);
                gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, source);
                verts.set([
                    ndcX(x0), ndcY(y0), u0, v0,
                    ndcX(x1), ndcY(y0), u1, v0,
                    ndcX(x0), ndcY(y1), u0, v1,
                    ndcX(x0), ndcY(y1), u0, v1,
                    ndcX(x1), ndcY(y0), u1, v0,
                    ndcX(x1), ndcY(y1), u1, v1,
                ]);
                gl.bufferData(gl.ARRAY_BUFFER, verts, gl.STREAM_DRAW);
                gl.drawArrays(gl.TRIANGLES, 0, 6);
            },
        };
    }

    function color(value)
    {
        value = (value || 0) >>> 0;
        return "#" + (value & 0xFFFFFF).toString(16).padStart(6, "0");
    }

    function charFor(chr)
    {
        if(text_charmap && text_charmap[chr])
        {
            return text_charmap[chr];
        }
        if(chr < 32)
        {
            return " ";
        }
        return String.fromCharCode(chr);
    }

    /**
     * Create a screenshot as ImageBitmap (works in workers)
     * @returns {Promise<ImageBitmap>}
     */
    this.make_screenshot = function()
    {
        return createImageBitmap(current_canvas);
    };

    this.init();
}

import React, { useEffect, useRef } from 'react';
import styled from 'styled-components';

const StyledCanvas = styled.canvas`
  position: fixed;
  top: 0;
  left: 0;
  width: 100%;
  height: 100%;
  z-index: -1;
`;

const Background = () => {
  const canvasRef = useRef<HTMLCanvasElement>(null);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;

    const ctx = canvas.getContext('2d');
    if (!ctx) return;

    const setDimensions = () => {
      canvas.width = window.innerWidth;
      canvas.height = window.innerHeight;
    };

    setDimensions();
    window.addEventListener('resize', setDimensions);

    class Particle {
      x: number;
      y: number;
      size: number;
      speedX: number;
      speedY: number;
      color: string;

      constructor() {
        this.x = Math.random() * (canvas?.width || 0);
        this.y = Math.random() * (canvas?.height || 0);
        this.size = Math.random() * 5 + 1;
        this.speedX = (Math.random() * 1.5) - 0.75; // Reduced speed range
        this.speedY = (Math.random() * 1.5) - 0.75; // Reduced speed range
        this.color = `rgba(255, 255, 255, ${Math.random() * 0.3 + 0.1})`; // Reduced opacity range
      }

      update() {
        this.x += this.speedX;
        this.y += this.speedY;

        if (this.x < 0) this.x = canvas?.width || 0;
        if (this.x > (canvas?.width || 0)) this.x = 0;
        if (this.y < 0) this.y = canvas?.height || 0;
        if (this.y > (canvas?.height || 0)) this.y = 0;
      }

      draw() {
        ctx!.fillStyle = this.color;
        ctx!.beginPath();
        ctx!.arc(this.x, this.y, this.size, 0, Math.PI * 2);
        ctx!.fill();
      }
    }

    const particlesArray: Particle[] = [];
    const particleCount = 100;

    for (let i = 0; i < particleCount; i++) {
      particlesArray.push(new Particle());
    }

    const animate = () => {
      ctx!.clearRect(0, 0, canvas!.width, canvas!.height);

      const gradient = ctx!.createLinearGradient(0, 0, canvas!.width, canvas!.height);
      gradient.addColorStop(0, '#1a1a2e');
      gradient.addColorStop(1, '#16213e');
      ctx!.fillStyle = gradient;
      ctx!.fillRect(0, 0, canvas!.width, canvas!.height);

      particlesArray.forEach(particle => {
        particle.update();
        particle.draw();
      });

      for (let i = 0; i < particlesArray.length; i++) {
        for (let j = i; j < particlesArray.length; j++) {
          const dx = particlesArray[i].x - particlesArray[j].x;
          const dy = particlesArray[i].y - particlesArray[j].y;
          const distance = Math.sqrt(dx * dx + dy * dy);

          if (distance < 100) {
            ctx!.beginPath();
            ctx!.strokeStyle = `rgba(255, 255, 255, ${0.2 - distance/500})`;
            ctx!.lineWidth = 0.5;
            ctx!.moveTo(particlesArray[i].x, particlesArray[i].y);
            ctx!.lineTo(particlesArray[j].x, particlesArray[j].y);
            ctx!.stroke();
          }
        }
      }

      requestAnimationFrame(animate);
    };

    animate();

    return () => {
      window.removeEventListener('resize', setDimensions);
    };
  }, []);

  return <StyledCanvas ref={canvasRef} />;
};

export default Background;
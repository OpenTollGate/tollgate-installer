import React from 'react';
import styled from 'styled-components';

interface ProgressBarProps {
  progress: number; // 0-100
  color?: 'primary' | 'success' | 'error' | 'warning' | 'info';
  height?: string;
  showPercentage?: boolean;
  className?: string;
}

const ProgressBarContainer = styled.div`
  width: 100%;
  background-color: ${props => props.theme.colors.backgroundAlt};
  border-radius: ${props => props.theme.radii.full};
  overflow: hidden;
  margin: 1rem 0;
`;

const ProgressBarFill = styled.div<{
  progress: number;
  color: string;
  height: string;
}>`
  height: ${props => props.height};
  width: ${props => props.progress}%;
  background-color: ${props => props.color};
  transition: width 0.3s ease-in-out;
  text-align: center;
  display: flex;
  align-items: center;
  justify-content: center;
`;

const ProgressLabel = styled.span<{ showBackground?: boolean }>`
  color: ${props => props.showBackground ? props.theme.colors.textLight : props.theme.colors.text};
  font-size: ${props => props.theme.fontSizes.sm};
  font-weight: ${props => props.theme.fontWeights.medium};
  padding: 0 0.5rem;
  white-space: nowrap;
`;

const ProgressBar: React.FC<ProgressBarProps> = ({
  progress,
  color = 'primary',
  height = '1rem',
  showPercentage = true,
  className
}) => {
  // Ensure progress is between 0-100
  const clampedProgress = Math.min(Math.max(progress, 0), 100);
  
  // Get color from theme based on color prop
  const getColor = (colorName: string) => {
    switch (colorName) {
      case 'success':
        return 'colors.success';
      case 'error':
        return 'colors.error';
      case 'warning':
        return 'colors.warning';
      case 'info':
        return 'colors.info';
      case 'primary':
      default:
        return 'colors.primary';
    }
  };

  return (
    <ProgressBarContainer className={className}>
      <ProgressBarFill
        progress={clampedProgress}
        color={getColor(color)}
        height={height}
      >
        {showPercentage && clampedProgress > 10 && (
          <ProgressLabel showBackground={true}>
            {Math.round(clampedProgress)}%
          </ProgressLabel>
        )}
      </ProgressBarFill>
      {showPercentage && clampedProgress <= 10 && (
        <ProgressLabel>
          {Math.round(clampedProgress)}%
        </ProgressLabel>
      )}
    </ProgressBarContainer>
  );
};

export default ProgressBar;